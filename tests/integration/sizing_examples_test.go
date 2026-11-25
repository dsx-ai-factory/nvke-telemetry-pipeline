// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kjson "k8s.io/apimachinery/pkg/util/json"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// TestSizingExamples checks independent coverage, baseline compatibility and
// ordinary Helm override behavior for the published sizing files.
func TestSizingExamples(t *testing.T) {
	read := func(t *testing.T, path string) map[string]interface{} {
		t.Helper()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		data, err = yaml.YAMLToJSON(data)
		require.NoError(t, err)
		var result map[string]interface{}
		require.NoError(t, kjson.Unmarshal(data, &result))
		return result
	}
	render := func(t *testing.T, args ...string) []byte {
		t.Helper()
		return command(t, "helm", append([]string{"template", "nktp", "../../nktp"}, args...)...)
	}
	smallPath := "../../docs/examples/sizing-small.values.yaml"
	largePath := "../../docs/examples/sizing-xlarge.values.yaml"
	small, large := read(t, smallPath), read(t, largePath)
	for name, values := range map[string]map[string]interface{}{"small": small, "xlarge": large} {
		t.Run(name+" required fields", func(t *testing.T) { assertRequiredSizing(t, values) })
	}
	require.Equal(t, sizingPaths(large, ""), sizingPaths(small, ""))
	require.Len(t, small, 8, "seven Collectors and KSM")
	baseline := render(t)
	require.Equal(t, string(baseline), string(render(t, "-f", largePath)))
	baselineSpecs := map[string]interface{}{}
	decoder := kyaml.NewYAMLOrJSONDecoder(bytes.NewReader(baseline), 4096)
	for {
		var resource unstructured.Unstructured
		err := decoder.Decode(&resource)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if resource.GetKind() == "OpenTelemetryCollector" {
			baselineSpecs[resource.GetName()] = resource.Object["spec"]
		}
	}
	// A distinct per-deployment value detects ignored or misrouted overrides.
	custom := read(t, smallPath)
	require.NoError(t, unstructured.SetNestedField(custom, "175m",
		"kube-state-metrics", "deployments", "workloads", "kubernetes", "resources", "requests", "cpu"))
	customValues, err := yaml.Marshal(custom)
	require.NoError(t, err)
	customPath := t.TempDir() + "/custom-sizing.yaml"
	require.NoError(t, os.WriteFile(customPath, customValues, 0600))
	for _, path := range []string{smallPath, largePath, customPath} {
		t.Run(path, func(t *testing.T) {
			overlay := read(t, path)
			decoder := kyaml.NewYAMLOrJSONDecoder(bytes.NewReader(render(t, "-f", path)), 4096)
			collectors, allocators, ksm := 0, 0, 0
			for {
				var resource unstructured.Unstructured
				err := decoder.Decode(&resource)
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err)
				if resource.GetKind() == "OpenTelemetryCollector" {
					name := strings.ReplaceAll(strings.TrimPrefix(resource.GetName(), "nktp-"), "-", "_")
					spec := resource.Object["spec"].(map[string]interface{})
					baselineSpec := baselineSpecs[resource.GetName()].(map[string]interface{})
					if name == "nodeless_metrics" {
						require.NotContains(t, overlay, name)
						require.Equal(t, baselineSpec, spec)
						continue
					}
					collectors++
					component := overlay[name].(map[string]interface{})
					require.NotContains(t, component, "sidecar")
					require.Equal(t, baselineSpec["initContainers"], spec["initContainers"])
					assertSizingSubset(t, component["kubernetes"], spec)
					kubernetes := component["kubernetes"].(map[string]interface{})
					if _, ok := kubernetes["targetAllocator"]; ok {
						allocators++
					}
					assertSizingSubset(t, component["otelcol"].(map[string]interface{})["config"], spec["config"])
				}
				if (resource.GetKind() == "Deployment" || resource.GetKind() == "DaemonSet") && strings.HasPrefix(resource.GetName(), "nktp-ksm-") {
					ksm++
					expected := overlay["kube-state-metrics"].(map[string]interface{})
					if resource.GetKind() == "DaemonSet" {
						expected = expected["daemonset"].(map[string]interface{})
					} else {
						name := strings.TrimPrefix(resource.GetName(), "nktp-ksm-")
						name = name[:strings.LastIndex(name, "-")] // Strip the shard suffix.
						deployments := expected["deployments"].(map[string]interface{})
						require.Contains(t, deployments, name)
						expected = deployments[name].(map[string]interface{})
					}
					containers, _, err := unstructured.NestedSlice(resource.Object, "spec", "template", "spec", "containers")
					require.NoError(t, err)
					assertSizingSubset(t, expected["kubernetes"].(map[string]interface{})["resources"], containers[0].(map[string]interface{})["resources"])
				}
			}
			require.Equal(t, 7, collectors)
			require.Equal(t, 3, allocators)
			require.Equal(t, 7, ksm)
		})
	}
	overrides := t.TempDir() + "/values.yaml"
	require.NoError(t, os.WriteFile(overrides, []byte(`gateway_logs:
  kubernetes:
    resources:
      requests:
        cpu: 75m
    autoscaler: null
    env:
      - name: GOGC
        value: "75"
  otelcol:
    config:
      exporters:
        otlp_grpc:
          sending_queue:
            queue_size: 12345
`), 0600))
	var gateway unstructured.Unstructured
	require.NoError(t, yaml.Unmarshal(render(t, "-f", smallPath, "-f", overrides, "--show-only", "templates/gateway-logs.yaml"), &gateway.Object))
	cpu, _, err := unstructured.NestedString(gateway.Object, "spec", "resources", "requests", "cpu")
	require.NoError(t, err)
	require.Equal(t, "75m", cpu)
	_, found, err := unstructured.NestedFieldNoCopy(gateway.Object, "spec", "autoscaler")
	require.NoError(t, err)
	require.False(t, found)
	env, _, err := unstructured.NestedSlice(gateway.Object, "spec", "env")
	require.NoError(t, err)
	require.Equal(t, []interface{}{map[string]interface{}{"name": "GOGC", "value": "75"}}, env)
	queue, _, err := unstructured.NestedFieldNoCopy(gateway.Object, "spec", "config", "exporters", "otlp_grpc", "sending_queue", "queue_size")
	require.NoError(t, err)
	require.EqualValues(t, 12345, queue)
}

func sizingPaths(values map[string]interface{}, prefix string) map[string]bool {
	paths := map[string]bool{}
	for key, value := range values {
		path := prefix + "/" + key
		if child, ok := value.(map[string]interface{}); ok {
			for leaf := range sizingPaths(child, path) {
				paths[leaf] = true
			}
		} else {
			paths[path] = true
		}
	}
	return paths
}

// Maps allow unrelated chart settings. Lists must retain every declared entry.
func assertSizingSubset(t *testing.T, expected, actual interface{}) {
	t.Helper()
	switch want := expected.(type) {
	case map[string]interface{}:
		got, ok := actual.(map[string]interface{})
		require.True(t, ok, "expected map, got %T", actual)
		for key, value := range want {
			assertSizingSubset(t, value, got[key])
		}
	case []interface{}:
		got, ok := actual.([]interface{})
		require.True(t, ok)
		require.Subset(t, got, want)
	default:
		require.EqualValues(t, expected, actual)
	}
}

// Required fields are independent of the examples so removing a field from both fails.
func assertRequiredSizing(t *testing.T, values map[string]interface{}) {
	t.Helper()
	fields := func(prefix string, suffixes ...string) {
		t.Helper()
		for _, suffix := range suffixes {
			path := prefix + "." + suffix
			value, found, err := unstructured.NestedFieldNoCopy(values, strings.Split(path, ".")...)
			require.NoError(t, err, path)
			require.True(t, found && value != nil, "missing sizing field: %s", path)
		}
	}
	resourceFields := []string{"resources.requests.cpu", "resources.requests.memory", "resources.limits.cpu", "resources.limits.memory"}
	for _, component := range []string{"gateway_logs", "gateway_metrics", "gateway_traces", "watchdog", "daemonset", "large_target_metrics", "cluster_events"} {
		prefix := component + ".kubernetes"
		fields(prefix, resourceFields...)
		env, found, err := unstructured.NestedSlice(values, component, "kubernetes", "env")
		require.NoError(t, err)
		require.True(t, found, "%s.env", prefix)
		names := map[string]bool{}
		for _, item := range env {
			entry, ok := item.(map[string]interface{})
			require.True(t, ok)
			name, ok := entry["name"].(string)
			require.True(t, ok)
			names[name] = true
		}
		require.Contains(t, names, "GOMEMLIMIT", component)
		require.Contains(t, names, "GOGC", component)
		if component == "daemonset" || component == "cluster_events" {
			require.Contains(t, names, "POD_NAMESPACE")
		}
		if component == "daemonset" {
			require.Contains(t, names, "NODE_NAME")
		} else {
			fields(prefix, "replicas")
		}
		if component != "daemonset" && component != "cluster_events" {
			fields(prefix+".autoscaler", "minReplicas", "maxReplicas", "targetCPUUtilization", "targetMemoryUtilization",
				"behavior.scaleUp.stabilizationWindowSeconds", "behavior.scaleUp.selectPolicy", "behavior.scaleUp.policies")
		}
		if component == "watchdog" || component == "daemonset" || component == "large_target_metrics" {
			fields(prefix+".targetAllocator", resourceFields...)
			fields(prefix, "targetAllocator.replicas")
		}
		exporters := []string{"otlp_grpc"}
		if component == "daemonset" {
			exporters = []string{"otlp_grpc/logs", "otlp_grpc/metrics", "otlp_grpc/traces", "otlp_grpc/watchdog"}
		}
		for _, exporter := range exporters {
			fields(component+".otelcol.config.exporters."+exporter, "sending_queue.queue_size")
		}
	}
	fields("kube-state-metrics.kubernetes", resourceFields...)
	fields("kube-state-metrics.daemonset.kubernetes", resourceFields...)
	for _, name := range []string{"unscheduled-pods", "workloads", "networking", "storage", "configuration", "cluster"} {
		prefix := "kube-state-metrics.deployments." + name
		fields(prefix+".kubernetes", resourceFields...)
		fields(prefix, "shards")
	}
}
