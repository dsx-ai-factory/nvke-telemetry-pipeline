// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestNodeLogCheckpoints(t *testing.T) {
	var writer, collector *corev1.Pod
	var coreClient coreclient.CoreV1Interface
	var patchFields func(context.Context, map[string]interface{}) error
	var stopCollector func(context.Context) error
	var startCollector func(context.Context) error
	var restoreCollector func(context.Context) error
	var originalConfig interface{}
	configChanged := false
	started := time.Now()
	id := envconf.RandomName("checkpoints", 28)
	const selector = "app.kubernetes.io/name=nktp-daemonset-collector"

	// Run serially because both cases interrupt a shared node Collector.
	// Requires a host journald socket and permission to update the Collector
	// resource and delete its Pods.
	runCase := func(persistent bool) func(context.Context, *testing.T, *envconf.Config) context.Context {
		return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			checkCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
			defer cancel()
			// Verify that the installed checkpointing recovers downtime logs.
			mode := "persistent"
			if !persistent {
				mode = "memory"
				// Disable checkpointing as a control: downtime logs must be lost,
				// demonstrating that recovery depends on saved read positions.
				// Apply this configuration before priming the readers.
				require.NoError(t, stopCollector(checkCtx))
				config := runtime.DeepCopyJSONValue(originalConfig).(map[string]interface{})
				for _, receiver := range []string{"file_log", "journald"} {
					unstructured.RemoveNestedField(config, "receivers", receiver, "storage")
					require.NoError(t, unstructured.SetNestedField(config, "end", "receivers", receiver, "start_at"))
				}
				configChanged = true
				require.NoError(t, patchFields(checkCtx, map[string]interface{}{"config": config}))
				require.NoError(t, startCollector(checkCtx))
			}
			expression := func(source, stage string) string {
				marker := id + "-" + mode + "-" + stage + "-" + source
				if source == "file" {
					return fmt.Sprintf(`{k8s_namespace_name=%q} | k8s_pod_name=%q |= %q`, c.Namespace(), writer.Name, marker)
				}
				return fmt.Sprintf(`{service_name="journald"} | k8s_node_name=%q |= %q`, writer.Spec.NodeName, marker)
			}
			write := func(stage string) {
				checkpointExec(checkCtx, t, c, writer, "sh", "-ec",
					`printf '%s\n' "$1" > /probe/input; logger -p user.info -t "$2" "$3"`,
					"sh", id+"-"+mode+"-"+stage+"-file", id, id+"-"+mode+"-"+stage+"-journal")
			}
			check := func(stage string) {
				for _, source := range []string{"file", "journal"} {
					require.NoError(t, wait.PollUntilContextCancel(checkCtx, time.Second, true, func(ctx context.Context) (bool, error) {
						return lokiHasLogsSince(ctx, coreClient.Services(c.Namespace()), expression(source, stage), started)
					}), "waiting for %s %s %s record in Loki", mode, stage, source)
				}
			}
			warmup := func(stage string) {
				// Health readiness can precede discovery of this container log
				// or journal startup. Repeated warmup records establish that
				// both readers are active before single-use records are written.
				require.NoError(t, wait.PollUntilContextCancel(checkCtx, time.Second, true, func(ctx context.Context) (bool, error) {
					write(stage)
					complete := true
					for _, source := range []string{"file", "journal"} {
						found, err := lokiHasLogsSince(ctx, coreClient.Services(c.Namespace()), expression(source, stage), started)
						if err != nil {
							return false, err
						}
						complete = complete && found
					}
					return complete, nil
				}), "waiting for %s readers to collect fresh records", mode)
			}
			// Stable initial content keeps file identification unchanged across
			// restarts while subsequent records are appended.
			checkpointExec(checkCtx, t, c, writer, "sh", "-ec", `printf '%1600s\n' seed > /probe/input`)
			warmup("before-warmup")
			write("before")
			check("before")

			require.NoError(t, stopCollector(checkCtx))
			write("offline")
			require.NoError(t, startCollector(checkCtx))
			warmup("after-warmup")
			write("after")
			check("after")
			if persistent {
				check("offline")
			} else {
				// Fresh records have traversed the pipeline. Keep checking for
				// delayed delivery of the downtime markers instead of treating
				// one empty query as proof they were skipped.
				const observationWindow = 30 * time.Second
				observingSince := time.Now()
				require.NoError(t, wait.PollUntilContextCancel(checkCtx, time.Second, true, func(ctx context.Context) (bool, error) {
					for _, source := range []string{"file", "journal"} {
						found, err := lokiHasLogsSince(ctx, coreClient.Services(c.Namespace()), expression(source, "offline"), started)
						if err != nil {
							return false, err
						}
						if found {
							return false, fmt.Errorf("%s downtime record recovered with checkpointing disabled", source)
						}
					}
					return time.Since(observingSince) >= observationWindow, nil
				}), "downtime records must remain absent while fresh records are collected")
			}
			return ctx
		}
	}

	f := features.NewWithDescription("NodeLogCheckpoints",
		"File and journal downtime records recover across same-node Collector Pod replacement with checkpointing, but are skipped without it. Teardown restores configuration and scheduling and removes the writer Pod.").
		WithLabel("execution", "serial").
		WithSetup("PrepareWriterAndCollectorOutage", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			var err error
			coreClient, err = coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err)
			pods, err := coreClient.Pods(c.Namespace()).List(setupCtx, metav1.ListOptions{LabelSelector: selector})
			require.NoError(t, err)
			for i := range pods.Items {
				pod := &pods.Items[i]
				ready, err := conditions.New(c.Client().Resources()).PodReady(pod)(setupCtx)
				require.NoError(t, err)
				if pod.DeletionTimestamp == nil && ready {
					collector = pod.DeepCopy()
					break
				}
			}
			require.NotNil(t, collector, "a ready node Collector is required")
			require.NotEmpty(t, collector.Spec.NodeName)
			node := collector.Spec.NodeName
			owner := metav1.GetControllerOf(collector)
			require.NotNil(t, owner)
			require.Equal(t, "DaemonSet", owner.Kind)
			daemonset := &appsv1.DaemonSet{}
			require.NoError(t, c.Client().Resources().Get(setupCtx, owner.Name, c.Namespace(), daemonset))
			collectorOwner := metav1.GetControllerOf(daemonset)
			require.NotNil(t, collectorOwner)
			require.Equal(t, "OpenTelemetryCollector", collectorOwner.Kind)
			// These test patches repeat admission warnings about the existing
			// configuration. Suppress them only on this client's requests.
			clientConfig := rest.CopyConfig(c.Client().RESTConfig())
			clientConfig.WarningHandlerWithContext = nil
			clientConfig.WarningHandler = rest.NoWarnings{}
			client, err := dynamic.NewForConfig(clientConfig)
			require.NoError(t, err)
			resource := client.Resource(schema.GroupVersionResource{Group: "opentelemetry.io", Version: "v1beta1", Resource: "opentelemetrycollectors"}).Namespace(c.Namespace())
			workload, err := resource.Get(setupCtx, collectorOwner.Name, metav1.GetOptions{})
			require.NoError(t, err)
			originalAffinity, _, err := unstructured.NestedFieldCopy(workload.Object, "spec", "affinity")
			require.NoError(t, err)
			originalStrategy, _, err := unstructured.NestedFieldCopy(workload.Object, "spec", "daemonSetUpdateStrategy")
			require.NoError(t, err)
			originalConfig, _, err = unstructured.NestedFieldCopy(workload.Object, "spec", "config")
			require.NoError(t, err)

			// Replace whole fields so OnDelete cannot retain incompatible
			// rollingUpdate settings, and restore absent fields by removing them.
			patchFields = func(ctx context.Context, fields map[string]interface{}) error {
				current, err := resource.Get(ctx, workload.GetName(), metav1.GetOptions{})
				if err != nil {
					return err
				}
				operations := []map[string]interface{}{}
				for name, value := range fields {
					if value == nil {
						if _, found, err := unstructured.NestedFieldNoCopy(current.Object, "spec", name); err != nil {
							return err
						} else if found {
							operations = append(operations, map[string]interface{}{"op": "remove", "path": "/spec/" + name})
						}
					} else {
						operations = append(operations, map[string]interface{}{"op": "add", "path": "/spec/" + name, "value": value})
					}
				}
				if len(operations) == 0 {
					return nil
				}
				patch, err := json.Marshal(operations)
				if err != nil {
					return err
				}
				updated, err := resource.Patch(ctx, workload.GetName(), types.JSONPatchType, patch, metav1.PatchOptions{})
				if err != nil {
					return err
				}
				// Wait for reconciliation before starting a Pod or checking
				// readiness, which might otherwise observe the old workload.
				return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
					current, err := resource.Get(ctx, workload.GetName(), metav1.GetOptions{})
					if err != nil {
						return false, err
					}
					observed, _, err := unstructured.NestedInt64(current.Object, "status", "observedGeneration")
					return observed >= updated.GetGeneration(), err
				})
			}
			restoreCollector = func(ctx context.Context) error {
				fields := map[string]interface{}{"affinity": originalAffinity, "daemonSetUpdateStrategy": originalStrategy}
				if configChanged {
					fields["config"] = originalConfig
				}
				return patchFields(ctx, fields)
			}
			excludedAffinity := &corev1.Affinity{}
			if originalAffinity != nil {
				data, err := json.Marshal(originalAffinity)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(data, excludedAffinity))
			}
			if excludedAffinity.NodeAffinity == nil {
				excludedAffinity.NodeAffinity = &corev1.NodeAffinity{}
			}
			hadRequiredAffinity := excludedAffinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil
			if !hadRequiredAffinity {
				excludedAffinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}}
			}
			for i := range excludedAffinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
				term := &excludedAffinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i]
				if hadRequiredAffinity && len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
					continue
				}
				term.MatchFields = append(term.MatchFields, corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{node}})
			}
			onDelete := appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}
			stopCollector = func(ctx context.Context) error {
				// Change scheduling through the owning Collector so the Operator
				// does not undo it. OnDelete preserves Pods on the other nodes.
				if err := patchFields(ctx, map[string]interface{}{"affinity": excludedAffinity, "daemonSetUpdateStrategy": onDelete}); err != nil {
					return err
				}
				if err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
					if err := c.Client().Resources().Get(ctx, daemonset.Name, c.Namespace(), daemonset); err != nil {
						return false, err
					}
					return daemonset.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType && reflect.DeepEqual(daemonset.Spec.Template.Spec.Affinity, excludedAffinity) && daemonset.Status.ObservedGeneration >= daemonset.Generation, nil
				}); err != nil {
					return err
				}
				// The controller may already have deleted this now-ineligible Pod.
				err := coreClient.Pods(c.Namespace()).Delete(ctx, collector.Name, metav1.DeleteOptions{})
				if err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
					pods, err := coreClient.Pods(c.Namespace()).List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "spec.nodeName=" + node})
					return err == nil && len(pods.Items) == 0, err
				})
			}
			startCollector = func(ctx context.Context) error {
				if err := patchFields(ctx, map[string]interface{}{"affinity": originalAffinity, "daemonSetUpdateStrategy": onDelete}); err != nil {
					return err
				}
				return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
					pods, err := coreClient.Pods(c.Namespace()).List(ctx, metav1.ListOptions{LabelSelector: selector, FieldSelector: "spec.nodeName=" + node})
					if err != nil {
						return false, err
					}
					for i := range pods.Items {
						pod := &pods.Items[i]
						if pod.UID == collector.UID || pod.DeletionTimestamp != nil {
							continue
						}
						ready, err := conditions.New(c.Client().Resources()).PodReady(pod)(ctx)
						if err != nil {
							return false, err
						}
						if ready {
							collector = pod.DeepCopy()
							return true, nil
						}
					}
					return false, nil
				})
			}

			writer = &corev1.Pod{}
			require.NoError(t, decoder.DecodeFile(os.DirFS("manifests/node-log-checkpoints"), "writer.yaml", writer, decoder.MutateNamespace(c.Namespace())))
			writer.Spec.NodeName = node
			writer.Spec.Containers[0].Image = passiveFixtureImage
			writer, err = coreClient.Pods(c.Namespace()).Create(setupCtx, writer, metav1.CreateOptions{})
			require.NoError(t, err)
			require.NoError(t, wait.PollUntilContextCancel(setupCtx, time.Second, true, conditions.New(c.Client().Resources()).PodReady(writer)))
			return ctx
		}).
		AssessWithDescription("RecoverDowntimeRecordsWithCheckpoints", "Both file and journal downtime records reach Loki after replacement, along with fresh records.", runCase(true)).
		AssessWithDescription("SkipDowntimeRecordsWithoutCheckpoints", "With storage disabled and start_at end, downtime records remain absent while fresh file and journal records reach Loki.", runCase(false)).
		WithTeardown("RestoreCollectorAndRemoveWriter", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			if restoreCollector != nil {
				if configChanged {
					// Recreate the selected reader with the original config
					// even when the installation itself uses OnDelete updates.
					if assert.NoError(t, stopCollector(cleanupCtx), "stopping the control reader") {
						if assert.NoError(t, patchFields(cleanupCtx, map[string]interface{}{"config": originalConfig}), "restoring checkpoint configuration") {
							assert.NoError(t, startCollector(cleanupCtx), "starting the restored reader")
						}
					}
				}
				assert.NoError(t, restoreCollector(cleanupCtx), "restoring Collector configuration and scheduling")
				_, err := waitForCollectors(cleanupCtx, c)
				assert.NoError(t, err, "waiting for Collectors after restoration")
			}
			if writer != nil && writer.Name != "" {
				err := c.Client().Resources().Delete(cleanupCtx, writer)
				if !apierrors.IsNotFound(err) {
					assert.NoError(t, err)
				}
			}
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

// checkpointExec runs a command in the dedicated writer Pod.
func checkpointExec(ctx context.Context, t *testing.T, c *envconf.Config, pod *corev1.Pod, command ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := c.Client().Resources().ExecInPod(ctx, c.Namespace(), pod.Name, "writer", command, &stdout, &stderr)
	require.NoError(t, err, "command %v: %s", command, stderr.String())
}
