// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA-dev/nktp/tests/internal/funcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestKubeStateMetrics(t *testing.T) {
	releaseNamespace := testenv.EnvConf().Namespace()
	type metricCheck struct {
		name, metric, label string
		want                []string
	}
	var checks []metricCheck
	var proxyClient coreclient.CoreV1Interface
	ns := envconf.RandomName("nktp-e2e-ksm", 48)
	volumeName := envconf.RandomName("nktp-e2e-ksm", 40)
	fixtureOptions := []decoder.DecodeOption{
		decoder.MutateNamespace(ns),
		decoder.MutateOption(func(obj k8s.Object) error {
			if claim, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				claim.Spec.VolumeName = volumeName
			}
			return nil
		}),
	}
	f := features.NewWithDescription("Kube-state metrics", "Checks fixture metrics for every default KSM collection group").
		WithSetup("CreateNamespace", funcs.CreateNamespace(ns)).
		WithSetup("EnsurePersistentVolumeName", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			var volume corev1.PersistentVolume
			for {
				err := c.Client().Resources().Get(ctx, volumeName, "", &volume)
				if apierrors.IsNotFound(err) {
					break
				}
				require.NoError(t, err, "checking whether PV %s exists", volumeName)
				volumeName = envconf.RandomName("nktp-e2e-ksm", 40)
			}
			return ctx
		}).
		WithSetup("ApplyResources", funcs.ApplyResources(FieldManager, "manifests/kube-state-metrics", "*.yaml", fixtureOptions...)).
		WithSetup("GenerateMetricQueries", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			var err error
			proxyClient, err = coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")

			for _, tc := range []struct{ group, metric, labels string }{
				{"cluster", "kube_namespace_status_phase", `phase="Active"`},
				{"configuration", "kube_configmap_info", `configmap="fixture"`},
				{"networking", "kube_service_info", `service="fixture"`},
				{"storage", "kube_persistentvolumeclaim_info", `persistentvolumeclaim="fixture"`},
				{"workloads", "kube_deployment_spec_replicas", `deployment="fixture"`},
				{"unscheduled-pods", "kube_pod_info", `pod="unscheduled",node=""`},
			} {
				checks = append(checks, metricCheck{
					name: tc.group,
					metric: fmt.Sprintf(`%s{namespace=%q,job=%q,nktp_metrics_pool="large-target",%s}`,
						tc.metric, ns, releaseNamespace+"/nktp-ksm-"+tc.group, tc.labels),
					label: "namespace",
					want:  []string{ns},
				})
			}

			var nodes corev1.NodeList
			err = c.Client().Resources().List(setupCtx, &nodes)
			require.NoError(t, err)
			require.NotEmpty(t, nodes.Items)
			require.NoError(t, funcs.WaitForDaemonSet(setupCtx, funcs.DaemonSetWaitOptions{
				Name: "node-fixture", Namespace: ns, Client: c.Client(), NodeCount: new(int32(len(nodes.Items))),
			}))
			var desiredNodes []string
			for _, node := range nodes.Items {
				desiredNodes = append(desiredNodes, node.Name)
			}
			checks = append(checks, metricCheck{
				name: "pods",
				metric: fmt.Sprintf(`kube_pod_info{namespace=%q,pod=~"node-fixture-.+",job=%q,nktp_metrics_pool=""}`,
					ns, releaseNamespace+"/nktp-ksm-pods"),
				label: "node",
				want:  desiredNodes,
			})

			return ctx
		}).
		AssessWithDescription("ValidateResourceMetricsArrived", "Every collection group delivers recent fixture metrics", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			found := make([]bool, len(checks))
			queryErrors := make([]error, len(checks))
			err := wait.For(func(ctx context.Context) (bool, error) {
				complete := true
				for i, check := range checks {
					// Filter for the expected gauge value and freshness before aggregation.
					expression := fmt.Sprintf(`count by (%s) ((%s == 1) and (timestamp(%s) > time() - %d))`,
						check.label, check.metric, check.metric, int(componentMetricMaxAge.Seconds()))
					values, queryErr := prometheusMetricLabelValues(ctx, proxyClient.Services(releaseNamespace), expression, check.label)
					queryErrors[i] = queryErr
					found[i] = queryErr == nil && coversValues(values, check.want)
					complete = complete && found[i]
				}
				return complete, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			for i, check := range checks {
				if assert.NoError(t, queryErrors[i], "querying %s", check.name) {
					assert.True(t, found[i], "KSM fixture metric missing, stale, or incorrect: %s; want %s values %v",
						check.metric, check.label, check.want)
				}
			}
			assert.NoError(t, err, "waiting for KSM metrics in LGTM")
			return ctx
		}).
		WithTeardown("DeleteNamespace", funcs.DeleteNamespace(ns)).
		Feature()
	testenv.Test(t, f)
}
