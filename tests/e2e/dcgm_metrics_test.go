// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestDCGMMetrics(t *testing.T) {
	// Requires a ready dcgm-exporter with Kubernetes pod mapping, a
	// ServiceMonitor with honorLabels=false, and an allocatable GPU.
	namespace := testenv.EnvConf().Namespace()
	var pod corev1.Pod
	created := false
	f := features.NewWithDescription("DCGMWorkloadMetrics",
		"A CUDA workload's GPU utilization reaches LGTM with its pod identity; teardown removes the workload.").
		WithSetup("CreateCUDAWorkload", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			var exporters corev1.PodList
			require.NoError(t, c.Client().Resources().List(setupCtx, &exporters,
				resources.WithLabelSelector("app.kubernetes.io/name=dcgm-exporter")), "listing dcgm-exporter pods")
			var exporter *corev1.Pod
			for i := range exporters.Items {
				candidate := &exporters.Items[i]
				if candidate.Status.Phase != corev1.PodRunning || candidate.DeletionTimestamp != nil {
					continue
				}
				if slices.ContainsFunc(candidate.Status.Conditions, func(condition corev1.PodCondition) bool {
					return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
				}) {
					exporter = candidate
					break
				}
			}
			if exporter == nil {
				t.Skip("dcgm-exporter is not installed and ready")
			}

			require.NoError(t, decoder.DecodeFile(os.DirFS("manifests/dcgm-metrics"), "pod.yaml", &pod,
				decoder.MutateNamespace(namespace)), "loading CUDA workload manifest")
			pod.Spec.RuntimeClassName = exporter.Spec.RuntimeClassName
			pod.Spec.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchFields: []corev1.NodeSelectorRequirement{{
								Key: "metadata.name", Operator: corev1.NodeSelectorOpIn,
								Values: []string{exporter.Spec.NodeName},
							}},
						}},
					},
				},
			}
			err := c.Client().Resources(namespace).Create(setupCtx, &pod)
			created = err == nil
			require.NoError(t, err, "creating CUDA workload")
			return ctx
		}).
		AssessWithDescription("CUDAWorkloadIsRunning", "The GPU workload starts successfully.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			require.NoError(t, waitFixturePodPhase(waitCtx, c.Client(), namespace, pod.Name, corev1.PodRunning))
			return ctx
		}).
		AssessWithDescription("GPUUtilizationReachesLGTM", "GPU utilization carries the workload pod label and CUDA vector addition succeeds.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			// The ServiceMonitor's target labels rename conflicting workload labels.
			query := fmt.Sprintf(`DCGM_FI_DEV_GPU_UTIL{exported_namespace=%q,exported_pod=%q,exported_container="cuda"} >= 0`,
				namespace, pod.Name)
			var got []string
			var lastErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				got, lastErr = prometheusMetricLabelValues(ctx, client.Services(namespace), query, "exported_pod")
				return lastErr == nil && slices.Contains(got, pod.Name), nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err, "waiting for GPU utilization from pod %s; got %v; last query error: %v", pod.Name, got, lastErr)
			var logs []byte
			var lastLogErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				logs, lastLogErr = client.Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "cuda", TailLines: new(int64(64))}).DoRaw(ctx)
				return lastLogErr == nil && strings.Contains(string(logs), "Test PASSED"), nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(time.Second), wait.WithImmediate())
			require.NoError(t, err, "reading CUDA workload logs; last log-read error: %v", lastLogErr)
			require.Contains(t, string(logs), "Test PASSED", "CUDA vector addition must succeed")
			return ctx
		}).
		WithTeardown("DeleteCUDAWorkload", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if !created {
				return ctx
			}
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute+30*time.Second)
			defer cancel()
			r := c.Client().Resources(namespace)
			err := r.Delete(cleanupCtx, &pod, func(opts *metav1.DeleteOptions) {
				opts.Preconditions = &metav1.Preconditions{UID: &pod.UID}
			})
			if !apierrors.IsNotFound(err) {
				require.NoError(t, err, "deleting CUDA workload %s/%s", namespace, pod.Name)
			}
			require.NoError(t, wait.For(conditions.New(r).ResourcesDeleted(&corev1.PodList{Items: []corev1.Pod{pod}}),
				wait.WithContext(cleanupCtx), wait.WithTimeout(0), wait.WithInterval(time.Second), wait.WithImmediate()),
				"waiting for CUDA workload %s/%s removal", namespace, pod.Name)
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
