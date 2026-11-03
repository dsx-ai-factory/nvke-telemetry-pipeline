// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestTargetAllocatorMTLS checks certificates and HTTPS configuration for every
// Collector/Target Allocator pair. New target requests must reach the mTLS handler.
// The nodeless pool asks for targets only while it has a job, so a labeled
// Probe stays applied for the whole feature.
func TestTargetAllocatorMTLS(t *testing.T) {
	namespace := testenv.EnvConf().Namespace()
	f := features.New("Target Allocator mTLS").
		Setup(funcs.ApplyResources(FieldManager, "manifests/target-allocator-mtls", "*.yaml", decoder.MutateNamespace(namespace))).
		Teardown(funcs.DeleteResources(FieldManager, "manifests/target-allocator-mtls", "*.yaml", decoder.MutateNamespace(namespace)))
	for _, collectorName := range []string{"nktp-daemonset", "nktp-large-target-metrics", "nktp-nodeless-metrics", "nktp-watchdog"} {
		metricServiceName := collectorName + "-targetallocator"
		if collectorName == "nktp-daemonset" {
			metricServiceName = "nktp-targetallocator"
		}
		f.Assess(collectorName, func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client := c.Client()
			r := client.Resources(c.Namespace())
			proxyClient, err := coreclient.NewForConfig(client.RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := proxyClient.Services(c.Namespace())

			var baseline map[string]float64
			var lastErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				baseline, lastErr = targetAllocatorRequestCounts(ctx, services)
				return lastErr == nil, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err, "reading Target Allocator request counts before checking for new requests: %v", lastErr)

			var collector unstructured.Unstructured
			collector.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "opentelemetry.io", Version: "v1beta1", Kind: "OpenTelemetryCollector",
			})
			if assert.NoError(t, r.Get(waitCtx, collectorName, c.Namespace(), &collector), "Collector %s should exist", collectorName) {
				enabled, found, nestedErr := unstructured.NestedBool(collector.Object, "spec", "targetAllocator", "mtls", "enabled")
				require.NoError(t, nestedErr)
				assert.True(t, found && enabled, "%s should enable Target Allocator mTLS", collectorName)

				useCertManager, found, nestedErr := unstructured.NestedBool(collector.Object, "spec", "targetAllocator", "mtls", "useCertManager")
				require.NoError(t, nestedErr)
				assert.True(t, found && useCertManager, "%s should explicitly use cert-manager", collectorName)
			}

			for _, suffix := range []string{"ca-cert", "ta-server-cert", "ta-client-cert"} {
				name := collectorName + "-" + suffix
				var certificate unstructured.Unstructured
				certificate.SetGroupVersionKind(schema.GroupVersionKind{
					Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
				})
				if assert.NoError(t, r.Get(waitCtx, name, c.Namespace(), &certificate), "Certificate %s should exist", name) {
					assert.True(t, resourceConditionTrue(certificate, "Ready"), "Certificate %s should be Ready", name)
				}
			}

			var service corev1.Service
			require.NoError(t, r.Get(waitCtx, collectorName+"-targetallocator", c.Namespace(), &service))
			assert.True(t, slices.ContainsFunc(service.Spec.Ports, func(port corev1.ServicePort) bool {
				return port.Name == "targetallocation-https" && port.Port == 443
			}), "%s should expose its mTLS endpoint", service.Name)

			var got map[string]float64
			err = wait.For(func(ctx context.Context) (bool, error) {
				got, lastErr = targetAllocatorRequestCounts(ctx, services)
				if lastErr != nil {
					return false, nil
				}
				return got[metricServiceName] > baseline[metricServiceName], nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			assert.NoError(t, err, "waiting for %s to request targets over mTLS: service %s count was %v, now %v; last query error: %v",
				collectorName, metricServiceName, baseline[metricServiceName], got[metricServiceName], lastErr)
			return ctx
		})
	}
	testenv.Test(t, f.Feature())
}

func resourceConditionTrue(resource unstructured.Unstructured, conditionType string) bool {
	conditions, found, err := unstructured.NestedSlice(resource.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == conditionType && condition["status"] == "True" {
			return true
		}
	}
	return false
}

const targetAllocatorRequestQuery = `sum by (service_name) (opentelemetry_allocator_http_duration_seconds_count{path="/jobs/:job_id/targets"})`

func targetAllocatorRequestCounts(ctx context.Context, services coreclient.ServiceInterface) (map[string]float64, error) {
	body, err := services.ProxyGet("http", "lgtm", "9090", "/api/v1/query", map[string]string{"query": targetAllocatorRequestQuery}).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus for Target Allocator requests: %w", err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding Target Allocator request counts: %w", err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus Target Allocator request query returned status %q: %s", response.Status, response.Error)
	}

	counts := make(map[string]float64, len(response.Data.Result))
	for _, result := range response.Data.Result {
		var value string
		if err := json.Unmarshal(result.Value[1], &value); err != nil {
			return nil, fmt.Errorf("decoding Target Allocator request count for %q: %w", result.Metric["service_name"], err)
		}
		count, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing Target Allocator request count for %q: %w", result.Metric["service_name"], err)
		}
		if math.IsNaN(count) || math.IsInf(count, 0) {
			return nil, fmt.Errorf("invalid Target Allocator request count %q for %q", value, result.Metric["service_name"])
		}
		counts[result.Metric["service_name"]] = count
	}
	return counts, nil
}
