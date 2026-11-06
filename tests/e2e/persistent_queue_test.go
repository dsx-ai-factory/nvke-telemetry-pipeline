// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestGatewayPersistence(t *testing.T) {
	var k kubernetes.Interface
	var senderPod *corev1.Pod
	var senderConfigMap *corev1.ConfigMap
	var restoreBackendConnectivity func(context.Context) error
	var gateways []corev1.Pod
	id := envconf.RandomName("persistence", 24)
	traceID := strings.ReplaceAll(uuid.NewString(), "-", "")
	const spanID = "0123456789abcdef"
	started := time.Now()
	// Run sequentially: this test interrupts the shared backend Service and
	// replaces gateway Pods. It requires the suite's provisioning StorageClass.
	f := features.NewWithDescription("GatewayPersistence",
		"Bound persistent queues retain metrics, logs, and traces across gateway replacement during a backend connection outage; teardown restores the Service and removes the sender Pod and ConfigMap.").
		WithLabel("execution", "serial").
		WithSetup("CheckStorageAndCreateSender", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			var err error
			k, err = kubernetes.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err)

			for _, component := range []string{"watchdog", "gateway-metrics", "gateway-logs", "gateway-traces"} {
				name := "nktp-" + component
				collector := &unstructured.Unstructured{}
				collector.SetGroupVersionKind(schema.GroupVersionKind{Group: "opentelemetry.io", Version: "v1beta1", Kind: "OpenTelemetryCollector"})
				require.NoError(t, c.Client().Resources(c.Namespace()).Get(setupCtx, name, c.Namespace(), collector))
				storage, found, err := unstructured.NestedString(collector.Object, "spec", "config", "exporters", "otlp_grpc", "sending_queue", "storage")
				require.NoError(t, err)
				require.True(t, found && (storage == "file_storage" || strings.HasPrefix(storage, "file_storage/")), "%s requires persistent sending_queue storage", name)
				directory, _, err := unstructured.NestedString(collector.Object, "spec", "config", "extensions", storage, "directory")
				require.NoError(t, err)
				require.NotEmpty(t, directory, name)
				pods, err := k.CoreV1().Pods(c.Namespace()).List(setupCtx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + name + "-collector"})
				require.NoError(t, err)
				require.NotEmpty(t, pods.Items, name)
				for _, pod := range pods.Items {
					claim := persistentQueueClaim(t, pod, directory)
					pvc, err := k.CoreV1().PersistentVolumeClaims(c.Namespace()).Get(setupCtx, claim, metav1.GetOptions{})
					require.NoError(t, err)
					require.Equal(t, corev1.ClaimBound, pvc.Status.Phase, claim)
				}
				if component != "watchdog" {
					gateways = append(gateways, pods.Items...)
				}
			}

			// Use the installed Collector image, retaining the sender's synchronous
			// exporters so an accepted post confirms gateway acceptance.
			source := &unstructured.Unstructured{}
			source.SetGroupVersionKind(schema.GroupVersionKind{Group: "opentelemetry.io", Version: "v1beta1", Kind: "OpenTelemetryCollector"})
			require.NoError(t, c.Client().Resources(c.Namespace()).Get(setupCtx, "nktp-daemonset", c.Namespace(), source))
			image, _, err := unstructured.NestedString(source.Object, "spec", "image")
			require.NoError(t, err)
			require.NotEmpty(t, image)
			manifests := os.DirFS("manifests/gateway-persistence")
			configMap := &corev1.ConfigMap{}
			require.NoError(t, decoder.DecodeFile(manifests, "configmap.yaml", configMap, decoder.MutateNamespace(c.Namespace())))
			senderConfigMap, err = k.CoreV1().ConfigMaps(c.Namespace()).Create(setupCtx, configMap, metav1.CreateOptions{})
			require.NoError(t, err)
			pod := &corev1.Pod{}
			require.NoError(t, decoder.DecodeFile(manifests, "pod.yaml", pod, decoder.MutateNamespace(c.Namespace())))
			pod.Spec.Containers[0].Image = image
			pod.Spec.Volumes[0].ConfigMap.Name = senderConfigMap.Name
			senderPod, err = k.CoreV1().Pods(c.Namespace()).Create(setupCtx, pod, metav1.CreateOptions{})
			require.NoError(t, err)
			require.NoError(t, wait.PollUntilContextCancel(setupCtx, time.Second, true,
				conditions.New(c.Client().Resources(c.Namespace())).PodReady(senderPod)))
			return ctx
		}).
		AssessWithDescription("PersistAcrossOutageAndRestart", "Every signal remains queued across gateway replacement and reaches its backend after reconnection.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			var err error
			restoreBackendConnectivity, err = simulateBackendConnectionOutage(waitCtx, k, c.Namespace())
			require.NoError(t, err, "simulating a backend connection outage")
			// Close existing gRPC connections so the Service change takes effect.
			restartGatewayPods(waitCtx, t, c, gateways)
			now := time.Now().UnixNano()
			resource := `{"attributes":[{"key":"service.name","value":{"stringValue":"nktp-e2e-persistence"}}]}`
			payloads := map[string]string{
				"metrics": fmt.Sprintf(`{"resourceMetrics":[{"resource":%s,"scopeMetrics":[{"metrics":[{"name":"nktp_e2e_persistent_queue","gauge":{"dataPoints":[{"attributes":[{"key":"test_run","value":{"stringValue":%q}}],"timeUnixNano":"%d","asInt":"1"}]}}]}]}]}`, resource, id, now),
				"logs":    fmt.Sprintf(`{"resourceLogs":[{"resource":%s,"scopeLogs":[{"logRecords":[{"timeUnixNano":"%d","body":{"stringValue":%q}}]}]}]}`, resource, now, id),
				"traces":  fmt.Sprintf(`{"resourceSpans":[{"resource":%s,"scopeSpans":[{"spans":[{"traceId":%q,"spanId":%q,"name":%q,"kind":1,"startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`, resource, traceID, spanID, id, now, now+int64(time.Millisecond)),
			}
			for _, signal := range []string{"metrics", "logs", "traces"} {
				var body []byte
				var lastErr error
				err := wait.PollUntilContextCancel(waitCtx, time.Second, true, func(ctx context.Context) (bool, error) {
					body, lastErr = k.CoreV1().RESTClient().Post().Namespace(c.Namespace()).Resource("pods").Name("http:"+senderPod.Name+":4318").SubResource("proxy").Suffix("v1", signal).
						SetHeader("Content-Type", "application/json").Body([]byte(payloads[signal])).DoRaw(ctx)
					return lastErr == nil, nil
				})
				require.NoError(t, err, "sending %s during the outage: %s; last error: %v", signal, body, lastErr)
				var response struct {
					PartialSuccess struct {
						RejectedDataPoints string `json:"rejectedDataPoints"`
						RejectedLogRecords string `json:"rejectedLogRecords"`
						RejectedSpans      string `json:"rejectedSpans"`
						ErrorMessage       string `json:"errorMessage"`
					} `json:"partialSuccess"`
				}
				require.NoError(t, json.Unmarshal(body, &response))
				for _, rejected := range []string{response.PartialSuccess.RejectedDataPoints, response.PartialSuccess.RejectedLogRecords, response.PartialSuccess.RejectedSpans} {
					require.Contains(t, []string{"", "0"}, rejected, "sender Pod must fully accept %s", signal)
				}
				require.Empty(t, response.PartialSuccess.ErrorMessage, signal)
			}
			for _, signal := range []string{"metrics", "logs", "traces"} {
				found, err := signalDelivered(waitCtx, k, c.Namespace(), signal, id, traceID, spanID, started)
				require.NoError(t, err)
				require.False(t, found, "%s reached the backend during the connection outage", signal)
			}
			// This replacement tests durability: accepted telemetry must survive
			// gateway process replacement while the backend is still unreachable.
			restartGatewayPods(waitCtx, t, c, gateways)
			require.NoError(t, restoreBackendConnectivity(waitCtx), "restoring backend connectivity")
			for _, signal := range []string{"metrics", "logs", "traces"} {
				var lastErr error
				err := wait.PollUntilContextCancel(waitCtx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
					var found bool
					found, lastErr = signalDelivered(ctx, k, c.Namespace(), signal, id, traceID, spanID, started)
					return found && lastErr == nil, nil
				})
				require.NoError(t, err, "waiting for persisted %s; last query error: %v", signal, lastErr)
			}
			return ctx
		}).
		WithTeardown("RestoreBackendAndRemoveSender", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			if k == nil {
				return ctx
			}
			if restoreBackendConnectivity != nil {
				assert.NoError(t, restoreBackendConnectivity(cleanupCtx))
				_, err := waitForCollectors(cleanupCtx, c)
				assert.NoError(t, err, "restoring healthy Collectors before other tests run")
			}
			if senderPod != nil {
				assert.NoError(t, k.CoreV1().Pods(c.Namespace()).Delete(cleanupCtx, senderPod.Name, metav1.DeleteOptions{}))
			}
			if senderConfigMap != nil {
				assert.NoError(t, k.CoreV1().ConfigMaps(c.Namespace()).Delete(cleanupCtx, senderConfigMap.Name, metav1.DeleteOptions{}))
			}
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

func persistentQueueClaim(t *testing.T, pod corev1.Pod, directory string) string {
	t.Helper()
	for _, container := range pod.Spec.Containers {
		if container.Name != "otc-container" {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if directory != mount.MountPath && !strings.HasPrefix(directory, mount.MountPath+"/") {
				continue
			}
			for _, volume := range pod.Spec.Volumes {
				if volume.Name == mount.Name && volume.PersistentVolumeClaim != nil {
					return volume.PersistentVolumeClaim.ClaimName
				}
			}
		}
	}
	t.Fatalf("%s storage directory %q must be on a PVC", pod.Name, directory)
	return ""
}

// simulateBackendConnectionOutage redirects OTLP to an unused port while
// leaving the backend Pod and its query ports available. Existing connections
// must be closed by the caller. The returned function restores the original
// target even when waiting for the outage's EndpointSlice update fails.
func simulateBackendConnectionOutage(ctx context.Context, k kubernetes.Interface, namespace string) (func(context.Context) error, error) {
	service, err := k.CoreV1().Services(namespace).Get(ctx, "lgtm", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var otlpPort *corev1.ServicePort
	for i := range service.Spec.Ports {
		if service.Spec.Ports[i].Port == 4317 {
			otlpPort = &service.Spec.Ports[i]
			break
		}
	}
	if otlpPort == nil {
		return nil, fmt.Errorf("lgtm Service has no OTLP/gRPC port")
	}
	restore := func(ctx context.Context) error {
		return setBackendOTLPTargetPort(ctx, k, namespace, otlpPort.TargetPort)
	}
	const unreachableOTLPPort = 1
	if err := setBackendOTLPTargetPort(ctx, k, namespace, intstr.FromInt32(unreachableOTLPPort)); err != nil {
		return restore, err
	}
	err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		endpoints, err := k.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=lgtm"})
		if err != nil {
			return false, err
		}
		for _, slice := range endpoints.Items {
			for _, port := range slice.Ports {
				if port.Name != nil && *port.Name == otlpPort.Name && port.Port != nil && *port.Port == unreachableOTLPPort {
					return true, nil
				}
			}
		}
		return false, nil
	})
	return restore, err
}

func setBackendOTLPTargetPort(ctx context.Context, k kubernetes.Interface, namespace string, port intstr.IntOrString) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		service, err := k.CoreV1().Services(namespace).Get(ctx, "lgtm", metav1.GetOptions{})
		if err != nil {
			return err
		}
		for i := range service.Spec.Ports {
			if service.Spec.Ports[i].Port == 4317 {
				service.Spec.Ports[i].TargetPort = port
				_, err = k.CoreV1().Services(namespace).Update(ctx, service, metav1.UpdateOptions{})
				return err
			}
		}
		return fmt.Errorf("lgtm Service has no OTLP/gRPC port")
	})
}

func restartGatewayPods(ctx context.Context, t *testing.T, c *envconf.Config, pods []corev1.Pod) {
	t.Helper()
	r := c.Client().Resources(c.Namespace())
	for i, old := range pods {
		require.NoError(t, r.Delete(ctx, &old, func(opts *metav1.DeleteOptions) {
			opts.Preconditions = &metav1.Preconditions{UID: &old.UID}
		}))
		replacement := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: old.Name, Namespace: c.Namespace()}}
		podReady := conditions.New(r).PodReady(replacement)
		require.NoError(t, wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			ready, err := podReady(ctx)
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if !ready || replacement.UID == old.UID || replacement.DeletionTimestamp != nil {
				return false, nil
			}
			pods[i] = *replacement
			return true, nil
		}), "waiting for replacement gateway %s", old.Name)
	}
}

func signalDelivered(ctx context.Context, k kubernetes.Interface, namespace, signal, id, traceID, spanID string, started time.Time) (bool, error) {
	switch signal {
	case "metrics":
		values, err := prometheusMetricLabelValues(ctx, k.CoreV1().Services(namespace), fmt.Sprintf(`max_over_time(nktp_e2e_persistent_queue{test_run=%q}[30m]) == 1`, id), "test_run")
		return slices.Contains(values, id), err
	case "logs":
		return lokiHasLogsSince(ctx, k.CoreV1().Services(namespace), fmt.Sprintf(`{service_name="nktp-e2e-persistence"} |= %q`, id), started)
	case "traces":
		response, err := queryTempoTrace(ctx, k.CoreV1().Services(namespace), traceID)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		trace, err := hex.DecodeString(traceID)
		if err != nil {
			return false, err
		}
		span, err := hex.DecodeString(spanID)
		if err != nil {
			return false, err
		}
		for _, resource := range response.Trace.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, item := range scope.Spans {
					if item.TraceID == base64.StdEncoding.EncodeToString(trace) && item.SpanID == base64.StdEncoding.EncodeToString(span) && item.Name == id {
						return true, nil
					}
				}
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("unknown signal %s", signal)
	}
}
