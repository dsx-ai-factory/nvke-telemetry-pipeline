// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestTraces sends a unique span through a node Collector and the trace gateway
// to Tempo, checking its identity, timestamps, service, and node metadata.
// Cleanup removes the disposable generator pod.
func TestTraces(t *testing.T) {
	const service = "nktp-e2e-traces"
	namespace := testenv.EnvConf().Namespace()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: service + "-", Namespace: namespace},
	}
	traceBytes := make([]byte, 16)
	spanBytes := make([]byte, 8)
	var traceID, spanID string
	var start, end int64
	var collector *corev1.Pod
	var proxyClient coreclient.CoreV1Interface

	f := features.New("Trace delivery").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client := c.Client()
			r := client.Resources(namespace)
			var err error
			proxyClient, err = coreclient.NewForConfig(client.RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")

			var collectors corev1.PodList
			require.NoError(t, r.List(setupCtx, &collectors,
				resources.WithLabelSelector("app.kubernetes.io/name=nktp-daemonset-collector")), "listing node Collectors")
			for i := range collectors.Items {
				candidate := &collectors.Items[i]
				if candidate.Status.Phase != corev1.PodRunning || candidate.DeletionTimestamp != nil {
					continue
				}
				for _, condition := range candidate.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						collector = candidate
						break
					}
				}
				if collector != nil {
					break
				}
			}
			require.NotNil(t, collector, "a running, ready node Collector that is not terminating must exist")
			require.NotEmpty(t, collector.Status.PodIP)
			require.NotEmpty(t, collector.Spec.NodeName)

			_, err = rand.Read(traceBytes)
			require.NoError(t, err)
			_, err = rand.Read(spanBytes)
			require.NoError(t, err)
			traceID, spanID = hex.EncodeToString(traceBytes), hex.EncodeToString(spanBytes)
			start = time.Now().UnixNano()
			end = start + int64(time.Millisecond)
			payload := fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":%q}}]},"scopeSpans":[{"scope":{"name":"nktp-e2e"},"spans":[{"traceId":%q,"spanId":%q,"name":"trace-delivery","kind":1,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`,
				service, traceID, spanID, start, end)

			template := passiveFixturePodTemplate(service)
			template.Spec.RestartPolicy = corev1.RestartPolicyNever
			template.Spec.NodeName = collector.Spec.NodeName
			generator := &template.Spec.Containers[0]
			generator.Name = "generator"
			generator.Env = []corev1.EnvVar{
				{Name: "PAYLOAD", Value: payload},
				{Name: "ENDPOINT", Value: "http://" + net.JoinHostPort(collector.Status.PodIP, "4318") + "/v1/traces"},
			}
			// Retry transient receiver failures, but stop after an accepted post.
			generator.Command = []string{"/bin/sh", "-ec", `
until wget -q -T 5 -O - --header 'Content-Type: application/json' --post-data "$PAYLOAD" "$ENDPOINT"; do
  sleep 2
done
`}
			pod.Labels = template.Labels
			pod.Spec = template.Spec
			pod, _ = funcs.CreateFixturePod(setupCtx, t, client, namespace, pod)
			return ctx
		}).
		Assess("Trace reaches Tempo with its span and resource attributes preserved", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			r := c.Client().Resources(namespace)
			t.Logf("waiting for trace %s from %s through Collector %s on node %s", traceID, pod.Name, collector.Name, collector.Spec.NodeName)
			var lastErr error
			err := wait.For(func(ctx context.Context) (bool, error) {
				var current corev1.Pod
				if err := r.Get(ctx, pod.Name, namespace, &current); err != nil {
					return false, err
				}
				if current.Status.Phase == corev1.PodFailed {
					return false, fmt.Errorf("trace generator failed: %+v", current.Status)
				}
				if current.Status.Phase != corev1.PodSucceeded {
					return false, nil
				}
				// Tempo returns base64 IDs for the hex OTLP/JSON IDs sent above.
				response, queryErr := queryTempoTrace(ctx, proxyClient.Services(namespace), traceID)
				lastErr = queryErr
				if queryErr != nil {
					return false, nil // A trace is initially absent while export completes.
				}
				for _, resource := range response.Trace.ResourceSpans {
					attributes := map[string]string{}
					for _, attribute := range resource.Resource.Attributes {
						attributes[attribute.Key] = attribute.Value.StringValue
					}
					if attributes["service.name"] != service || attributes["k8s.node.name"] != collector.Spec.NodeName {
						continue
					}
					for _, scope := range resource.ScopeSpans {
						for _, span := range scope.Spans {
							if span.TraceID == base64.StdEncoding.EncodeToString(traceBytes) &&
								span.SpanID == base64.StdEncoding.EncodeToString(spanBytes) && span.Name == "trace-delivery" &&
								span.Start == fmt.Sprint(start) && span.End == fmt.Sprint(end) {
								return true, nil
							}
						}
					}
				}
				lastErr = fmt.Errorf("Tempo returned trace %s without the expected span and resource attributes: %+v", traceID, response)
				return false, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err, "waiting for trace %s in Tempo; last query error: %v", traceID, lastErr)
			return ctx
		}).
		Feature()
	testenv.Test(t, f)
}

type tempoTraceResponse struct {
	Trace struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID string `json:"traceId"`
					SpanID  string `json:"spanId"`
					Name    string `json:"name"`
					Start   string `json:"startTimeUnixNano"`
					End     string `json:"endTimeUnixNano"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	} `json:"trace"`
}

// queryTempoTrace reads a trace through LGTM's Grafana datasource proxy.
// Tempo represents the OTLP trace and span IDs as base64 in this response.
func queryTempoTrace(ctx context.Context, services coreclient.ServiceInterface, traceID string) (tempoTraceResponse, error) {
	var response tempoTraceResponse
	body, err := services.ProxyGet("http", "lgtm", "3000",
		"/api/datasources/proxy/uid/tempo/api/v2/traces/"+traceID, nil).DoRaw(ctx)
	if err != nil {
		return response, err
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return response, fmt.Errorf("decoding Tempo trace %s: %w", traceID, err)
	}
	return response, nil
}
