// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestKubeletMetrics proves that every node sends both kubelet and cAdvisor
// metrics to Prometheus. A passive, digest-pinned NVIDIA container on each node
// gives cAdvisor a real workload whose memory metric must be non-zero.
func TestKubeletMetrics(t *testing.T) {
	name := "nktp-e2e-kubelet-metrics"
	namespace := testenv.EnvConf().Namespace()
	var desiredNodes []string
	f := features.New("Kubelet and cAdvisor metrics").
		Setup(funcs.ApplyResources(FieldManager, "manifests/kubelet-metrics", "*.yaml", decoder.MutateNamespace(namespace))).
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			var nodes corev1.NodeList
			err := c.Client().Resources().List(ctx, &nodes)
			require.NoError(t, err, "listing cluster nodes")
			for _, n := range nodes.Items {
				desiredNodes = append(desiredNodes, n.Name)
			}
			slices.Sort(desiredNodes)
			require.NotEmpty(t, desiredNodes, "cluster has no nodes")
			return ctx
		}).
		Assess("Daemonset readiness", funcs.DaemonsetReady(name)).
		Assess("Every node exports kubelet metrics", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := client.Services(namespace)
			query := "count by (node) (kubelet_active_pods > 0)"
			var got []string
			var lastErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				got, lastErr = prometheusMetricLabelValues(ctx, services, query, "node")
				if lastErr != nil {
					return false, nil
				}
				return coversValues(got, desiredNodes), nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err,
				"want nodes %v; kubelet_active_pods nodes %v; last query error: %v",
				desiredNodes, got, lastErr)
			return ctx
		}).
		Assess("Every node exports cAdvisor metrics", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := client.Services(namespace)
			query := fmt.Sprintf(
				`max by (node) (container_memory_working_set_bytes{namespace=%q,pod=~%q,container="memory"}) > 0`,
				namespace, name+"-.+")
			var got []string
			var lastErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				got, lastErr = prometheusMetricLabelValues(ctx, services, query, "node")
				if lastErr != nil {
					return false, nil
				}
				return coversValues(got, desiredNodes), nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err,
				"want nodes %v; container_memory_working_set_bytes nodes %v; last query error: %v",
				desiredNodes, got, lastErr)
			return ctx
		}).
		Teardown(funcs.DeleteResources(FieldManager, "manifests/kubelet-metrics", "*.yaml", decoder.MutateNamespace(namespace))).
		Feature()
	testenv.Test(t, f)
}

func coversValues(got, want []string) bool {
	for _, expected := range want {
		if !slices.Contains(got, expected) {
			return false
		}
	}
	return true
}

func prometheusMetricLabelValues(ctx context.Context, services coreclient.ServiceInterface, expression, label string) ([]string, error) {
	body, err := services.ProxyGet("http", "lgtm", "9090", "/api/v1/query", map[string]string{"query": expression}).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus for %q: %w", expression, err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding Prometheus response for %q: %w", expression, err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus query %q returned status %q: %s", expression, response.Status, response.Error)
	}

	values := make([]string, 0, len(response.Data.Result))
	for _, result := range response.Data.Result {
		if value := result.Metric[label]; value != "" {
			values = append(values, value)
		}
	}
	slices.Sort(values)
	return slices.Compact(values), nil
}
