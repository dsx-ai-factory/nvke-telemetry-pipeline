// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const journalFixtureName = "nktp-e2e-journald"

// TestJournaldLogs verifies journal fields, node metadata, auth classification,
// priority filtering, and credential redaction through Loki on every node.
// After delivery, it checks another 30 seconds of writes for forbidden records.
// It requires the runtime journal mount configured by the e2e values and skips
// if any node lacks a journald socket. Cleanup removes all fixture pods.
func TestJournaldLogs(t *testing.T) {
	type journalCheck struct {
		node, name, expression string
		forbidden              bool
		found                  bool
		queryErr               error
	}
	var checks []journalCheck
	var started time.Time
	var proxyClient coreclient.CoreV1Interface
	var pods []*corev1.Pod
	f := features.New("Journald logs").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			var err error
			proxyClient, err = coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			started = time.Now()
			marker := envconf.RandomName(journalFixtureName, 48)

			var nodes corev1.NodeList
			err = c.Client().Resources().List(setupCtx, &nodes)
			require.NoError(t, err, "listing nodes for the journald fixture")
			require.NotEmpty(t, nodes.Items, "the cluster must have a node to test")

			for _, node := range nodes.Items {
				template := passiveFixturePodTemplate(journalFixtureName)
				template.Spec.NodeName = node.Name
				template.Spec.RestartPolicy = corev1.RestartPolicyNever
				template.Spec.SecurityContext.RunAsNonRoot = new(false)
				template.Spec.Volumes = []corev1.Volume{{
					Name: "hostrun", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
						Path: "/run", Type: new(corev1.HostPathDirectory),
					}},
				}}
				container := &template.Spec.Containers[0]
				container.SecurityContext.RunAsUser = new(int64(0))
				container.VolumeMounts = []corev1.VolumeMount{{Name: "hostrun", MountPath: "/hostrun", ReadOnly: true}}
				container.Command = []string{"/bin/sh", "-ec", `
if [ ! -S /hostrun/systemd/journal/dev-log ]; then
  printf '%s\n' /run/systemd/journal/dev-log
fi
`}
				probe, removeProbe := funcs.CreateFixturePod(setupCtx, t, c.Client(), c.Namespace(), &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{GenerateName: journalFixtureName + "-preflight-", Labels: template.Labels},
					Spec:       template.Spec,
				})
				require.NoError(t, waitFixturePodPhase(setupCtx, c.Client(), c.Namespace(), probe.Name, corev1.PodSucceeded),
					"checking the journald socket on %s", node.Name)
				output, err := proxyClient.Pods(c.Namespace()).GetLogs(probe.Name, &corev1.PodLogOptions{}).DoRaw(setupCtx)
				require.NoError(t, err, "reading journald preflight on %s", node.Name)
				removeProbe()
				if path := strings.TrimSpace(string(output)); path != "" {
					t.Skipf("journald socket unavailable on %s: %s", node.Name, path)
				}
			}

			pods = make([]*corev1.Pod, 0, len(nodes.Items))
			for _, node := range nodes.Items {
				pod := createJournalFixture(setupCtx, t, c.Client(), c.Namespace(), node.Name, marker)
				pods = append(pods, pod)
			}

			for _, pod := range pods {
				// Distinguish journal records from fixture container logs.
				selector := fmt.Sprintf(`{service_name="journald"} | k8s_node_name=%q |= %q`, pod.Spec.NodeName, marker)
				fields := selector + ` | json journal_message="MESSAGE", journal_identifier="SYSLOG_IDENTIFIER", journal_priority="PRIORITY"`
				for _, tc := range []struct {
					name, identifier, priority, messageFilter string
				}{
					{"info", journalFixtureName, "6", fmt.Sprintf(`journal_message=%q | nvidia_telemetry_class=""`, marker+"-info")},
					{"warning", journalFixtureName, "4", fmt.Sprintf(`journal_message=%q`, marker+"-warning")},
					{"error", journalFixtureName, "3", fmt.Sprintf(`journal_message=%q`, marker+"-error")},
					{"auth", "sudo", "6", fmt.Sprintf(`journal_message=%q | nvidia_telemetry_class="system.auth"`, marker+"-auth")},
					{"password-redaction", journalFixtureName, "6", fmt.Sprintf(`journal_message=~%q`, "^"+regexp.QuoteMeta(marker+"-password")+` \*+ safe=kept$`)},
					{"bearer-redaction", journalFixtureName, "6", fmt.Sprintf(`journal_message=~%q`, "^"+regexp.QuoteMeta(marker+"-credential")+` \*+ safe=kept$`)},
				} {
					checks = append(checks, journalCheck{
						node: pod.Spec.NodeName, name: tc.name,
						expression: fmt.Sprintf(`%s | journal_identifier=%q | journal_priority=%q | %s`, fields, tc.identifier, tc.priority, tc.messageFilter),
					})
				}
				checks = append(checks,
					journalCheck{node: pod.Spec.NodeName, name: "debug-filtering", forbidden: true,
						expression: selector + fmt.Sprintf(` |= %q`, marker+"-debug")},
					journalCheck{node: pod.Spec.NodeName, name: "unmasked-credentials", forbidden: true,
						expression: selector + ` |~ "nktp-e2e-fake-password|nktp-e2e-fake-credential"`},
				)
			}

			return ctx
		}).
		Assess("Journal records preserve fields, filtering, and redaction", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			const observationWindow = 30 * time.Second
			var observingSince time.Time
			err := wait.For(func(ctx context.Context) (bool, error) {
				for _, pod := range pods {
					var current corev1.Pod
					err := c.Client().Resources(c.Namespace()).Get(ctx, pod.Name, c.Namespace(), &current)
					if err != nil {
						return false, err
					}
					if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
						return false, fmt.Errorf("journal fixture %s on %s stopped: %+v", pod.Name, pod.Spec.NodeName, current.Status)
					}
				}
				complete := true
				for i := range checks {
					check := &checks[i]
					if check.found && !check.forbidden {
						continue
					}
					check.found, check.queryErr = lokiHasLogsSince(ctx, proxyClient.Services(c.Namespace()), check.expression, started)
					if check.forbidden {
						if check.queryErr != nil {
							return false, fmt.Errorf("checking %s on node %s: %w", check.name, check.node, check.queryErr)
						}
						if check.found {
							return false, fmt.Errorf("forbidden %s record reached Loki on node %s", check.name, check.node)
						}
					} else {
						complete = complete && check.found && check.queryErr == nil
					}
				}
				if complete && observingSince.IsZero() {
					observingSince = time.Now()
					t.Logf("all expected journal records arrived; observing priority filtering and redaction for %s", observationWindow)
				}
				return complete && time.Since(observingSince) >= observationWindow, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			for _, check := range checks {
				if assert.NoError(t, check.queryErr, "querying %s/%s journald logs in Loki", check.node, check.name) {
					if check.forbidden {
						assert.False(t, check.found, "forbidden journal record reached Loki: %s", check.expression)
					} else {
						assert.True(t, check.found, "expected journal record did not reach Loki: %s", check.expression)
					}
				}
			}
			assert.NoError(t, err, "polling journald logs and observing filtered records")
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

func createJournalFixture(ctx context.Context, t *testing.T, client klient.Client, namespace, node, marker string) *corev1.Pod {
	t.Helper()
	template := passiveFixturePodTemplate(journalFixtureName)
	template.Spec.NodeName = node
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.Volumes = []corev1.Volume{{
		Name: "journal-socket",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: "/run/systemd/journal/dev-log", Type: new(corev1.HostPathSocket),
		}},
	}}
	generator := &template.Spec.Containers[0]
	generator.Name = "logger"
	generator.VolumeMounts = []corev1.VolumeMount{{Name: "journal-socket", MountPath: "/dev/log", ReadOnly: true}}
	generator.Env = []corev1.EnvVar{{Name: "JOURNAL_MESSAGE", Value: marker}}
	// Keep the credential marker and fake value free of "bearer" so the
	// case-insensitive redaction pattern matches only the credential itself.
	generator.Command = []string{"/bin/sh", "-ec", `
while true; do
  logger -p user.debug -t nktp-e2e-journald "$JOURNAL_MESSAGE-debug"
  logger -p user.info -t nktp-e2e-journald "$JOURNAL_MESSAGE-info"
  logger -p user.warning -t nktp-e2e-journald "$JOURNAL_MESSAGE-warning"
  logger -p user.err -t nktp-e2e-journald "$JOURNAL_MESSAGE-error"
  logger -p auth.info -t sudo "$JOURNAL_MESSAGE-auth"
  logger -p user.info -t nktp-e2e-journald "$JOURNAL_MESSAGE-password password=nktp-e2e-fake-password safe=kept"
  logger -p user.info -t nktp-e2e-journald "$JOURNAL_MESSAGE-credential Bearer nktp-e2e-fake-credential safe=kept"
  sleep 2
done
`}
	pod, _ := funcs.CreateFixturePod(ctx, t, client, namespace, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: journalFixtureName + "-", Labels: template.Labels},
		Spec:       template.Spec,
	})
	t.Logf("journal fixture %s on node %s: message=%s", pod.Name, node, marker)
	return pod
}
