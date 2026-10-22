// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestNVSentinelMetrics(t *testing.T) {
	// Requires the listed components with PodMonitor discovery enabled.
	// Janitor may use manual mode; zero-valued counters need no remediation.
	cases := []struct{ component, container, metric string }{
		{"GPU health monitor API", "gpu-health-monitor", "dcgm_api_latency_bucket"},
		{"GPU health monitor reconcile", "gpu-health-monitor", "dcgm_reconcile_time_bucket"},
		{"MongoDB", "metrics", "mongodb_up"},
		{"Fault Quarantine", "fault-quarantine", "fault_quarantine_events_received_total"},
		{"Node Drainer", "node-drainer", "node_drainer_events_received_total"},
		{"Fault Remediation", "fault-remediation", "fault_remediation_events_received_total"},
		// Completed-action metrics need a remediation; these exist while idle.
		{"Janitor", "janitor", "controller_runtime_active_workers"},
		{"Janitor Provider", "janitor-provider", "go_goroutines"},
		{"Labeler", "labeler", "labeler_events_processed_total"},
		{"Metadata Collector", "metadata-collector", "metadata_collector_pod_mapper_failures_total"},
		{"Syslog Health Monitor", "syslog-health-monitor", "syslog_health_monitor_xid_errors_total"},
		{"Platform Connector", "platform-connector", "platform_connector_health_events_received_total"},
	}
	var namespace string
	var cancelMetrics context.CancelFunc
	f := features.NewWithDescription("NVSentinelMetrics",
		"Each NVSentinel component delivers a recent metric to LGTM; skips when NVSentinel is absent.").
		WithSetup("DiscoverNVSentinelNamespace", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			var pods corev1.PodList
			require.NoError(t, c.Client().Resources().List(setupCtx, &pods,
				resources.WithLabelSelector("app.kubernetes.io/name=nvsentinel")), "listing NVSentinel platform connectors")
			if len(pods.Items) == 0 {
				t.Skip("NVSentinel is not installed")
			}
			namespace = pods.Items[0].Namespace
			return ctx
		}).
		AssessWithDescription("EachComponentExportsRecentMetric", "Every NVSentinel component exports a metric within the allowed age.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			cancelMetrics = cancel
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			for _, tc := range cases {
				t.Run(tc.component, func(t *testing.T) {
					t.Parallel()
					expression := fmt.Sprintf(`timestamp(%s{namespace=%q,container=%q}) > time() - %d`,
						tc.metric, namespace, tc.container, int(componentMetricMaxAge.Seconds()))
					var containers []string
					var queryErr error
					err := wait.For(func(ctx context.Context) (bool, error) {
						containers, queryErr = prometheusMetricLabelValues(ctx, client.Services(c.Namespace()), expression, "container")
						return queryErr == nil && slices.Contains(containers, tc.container), nil
					}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
					require.NoError(t, err, "waiting for recent %s from %s in namespace %s; last query error: %v",
						tc.metric, tc.container, namespace, queryErr)
				})
			}
			return ctx
		}).
		WithTeardown("CancelMetricQueries", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			// Teardown runs after the parallel metric checks finish.
			if cancelMetrics != nil {
				cancelMetrics()
			}
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
