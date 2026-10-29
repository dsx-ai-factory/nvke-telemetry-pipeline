// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestPersistentQueueClaims checks that the installed watchdog and gateways
// mount bound per-replica claims in their main containers. It requires the
// suite cluster's default StorageClass. The test creates no additional resources.
func TestPersistentQueueClaims(t *testing.T) {
	f := features.New("Persistent exporter queue volumes").
		Assess("Collector Pods mount bound queue claims", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			client, err := kubernetes.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err)
			for _, name := range []string{"watchdog", "gateway-logs", "gateway-metrics", "gateway-traces"} {
				pods, err := client.CoreV1().Pods(c.Namespace()).List(ctx, metav1.ListOptions{
					LabelSelector: "app.kubernetes.io/name=nktp-" + name + "-collector",
				})
				require.NoError(t, err)
				require.NotEmpty(t, pods.Items, name)
				for _, pod := range pods.Items {
					claimName := ""
					for _, volume := range pod.Spec.Volumes {
						if volume.Name == "queue" {
							require.NotNil(t, volume.PersistentVolumeClaim, pod.Name)
							claimName = volume.PersistentVolumeClaim.ClaimName
						}
					}
					require.NotEmpty(t, claimName, pod.Name)
					claim, err := client.CoreV1().PersistentVolumeClaims(c.Namespace()).Get(ctx, claimName, metav1.GetOptions{})
					require.NoError(t, err)
					require.Equal(t, corev1.ClaimBound, claim.Status.Phase, claimName)
					require.NotEmpty(t, claim.Spec.VolumeName, claimName)
					capacity := resource.MustParse("1Gi")
					if name == "watchdog" {
						capacity = resource.MustParse("3Gi")
					}
					require.True(t, claim.Status.Capacity.Storage().Cmp(capacity) >= 0, claimName)
					for _, container := range pod.Spec.Containers {
						if container.Name == "otc-container" {
							require.Contains(t, container.VolumeMounts, corev1.VolumeMount{
								Name: "queue", MountPath: "/var/lib/otelcol/queue",
							}, pod.Name)
						}
					}
				}
			}
			return ctx
		})
	testenv.Test(t, f.Feature())
}
