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
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestHostFileLogs verifies host authentication and audit logs on every node,
// including their metadata and credential redaction. After delivery, it checks
// another 30 seconds of writes for unmasked fake credentials.
// The test skips if target log paths already exist. Cleanup stops writers and
// removes only fixture-owned files and directories.
func TestHostFileLogs(t *testing.T) {
	releaseNamespace := testenv.EnvConf().Namespace()
	type logCheck struct {
		name, expression string
		forbidden, found bool
		queryErr         error
	}
	var checks []logCheck
	var started time.Time
	var proxyClient coreclient.CoreV1Interface
	ns := envconf.RandomName("nktp-e2e-hostfiles", 48)
	var writers []*corev1.Pod
	f := features.New("Host file logs").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			var err error
			proxyClient, err = coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			setupCtx, err = envfuncs.CreateNamespace(ns)(setupCtx, c)
			require.NoError(t, err, "creating fixture namespace")
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(setupCtx), 30*time.Second)
				defer cancel()
				_, err := envfuncs.DeleteNamespace(ns)(cleanupCtx, c)
				assert.NoError(t, err, "deleting fixture namespace %s", ns)
			})
			started = time.Now()
			marker := envconf.RandomName("nktp-e2e-hostfiles", 48)
			var nodes corev1.NodeList
			err = c.Client().Resources().List(setupCtx, &nodes)
			require.NoError(t, err)
			require.NotEmpty(t, nodes.Items)

			for _, node := range nodes.Items {
				probe := hostFileFixturePod(node.Name, marker, hostFilesPreflightScript)
				probe.Spec.Containers[0].VolumeMounts[0].ReadOnly = true
				probe, removeProbe := funcs.CreateFixturePod(setupCtx, t, c.Client(), ns, probe)
				require.NoError(t, waitFixturePodPhase(setupCtx, c.Client(), ns, probe.Name, corev1.PodSucceeded),
					"checking existing host log paths on %s", node.Name)
				output, err := proxyClient.Pods(ns).GetLogs(probe.Name, &corev1.PodLogOptions{}).DoRaw(setupCtx)
				require.NoError(t, err, "reading host log preflight on %s", node.Name)
				removeProbe()
				if paths := strings.TrimSpace(string(output)); paths != "" {
					t.Skipf("host log paths already exist on %s: %s", node.Name, paths)
				}
			}

			for _, node := range nodes.Items {
				// Stop the writer before removing its files.
				t.Cleanup(func() {
					cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(setupCtx), 15*time.Minute+30*time.Second)
					defer cancel()
					cleaner, _ := funcs.CreateFixturePod(cleanupCtx, t, c.Client(), ns, hostFileFixturePod(node.Name, marker, hostFilesCleanupScript))
					require.NoError(t, waitFixturePodPhase(cleanupCtx, c.Client(), ns, cleaner.Name, corev1.PodSucceeded), "removing host fixture files on %s", node.Name)
				})
				writer, _ := funcs.CreateFixturePod(setupCtx, t, c.Client(), ns, hostFileFixturePod(node.Name, marker, hostFilesWriterScript))
				writers = append(writers, writer)
				t.Logf("host-file writer %s/%s on %s: marker=%s", ns, writer.Name, node.Name, marker)

				for _, source := range []struct{ name, path, service, class string }{
					{"authlog", "/hostfs/var/log/auth.log", "auth", "system.auth"},
					{"secure", "/hostfs/var/log/secure", "auth", "system.auth"},
					{"audit", "/hostfs/var/log/audit/audit.log", "auditd", "system.audit"},
				} {
					selector := fmt.Sprintf(`{service_name=%q} | k8s_node_name=%q | nvidia_telemetry_class=%q | log_file_path=%q`,
						source.service, node.Name, source.class, source.path)
					selector += fmt.Sprintf(` |= %q`, marker+"-"+source.name)
					checks = append(checks,
						logCheck{name: node.Name + "/" + source.name + "/delivery",
							expression: selector + fmt.Sprintf(` |~ %q`, "^"+regexp.QuoteMeta(marker+"-"+source.name+" plain")+"$")},
						logCheck{name: node.Name + "/" + source.name + "/redaction",
							expression: selector + fmt.Sprintf(` |~ %q`, "^"+regexp.QuoteMeta(marker+"-"+source.name+" credential")+` \*+ \*+ safe=kept$`)},
						logCheck{name: node.Name + "/" + source.name + "/unmasked-credentials", forbidden: true,
							expression: selector + ` |~ "nktp-e2e-fake-password|nktp-e2e-fake-credential"`},
					)
				}
			}

			return ctx
		}).
		Assess("Host records preserve fields and redact credentials", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			var observingSince time.Time
			const observationWindow = 30 * time.Second
			err := wait.For(func(ctx context.Context) (bool, error) {
				for _, pod := range writers {
					var current corev1.Pod
					err := c.Client().Resources(ns).Get(ctx, pod.Name, ns, &current)
					if err != nil {
						return false, err
					}
					if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
						return false, fmt.Errorf("host-file writer %s on %s stopped: %+v", pod.Name, pod.Spec.NodeName, current.Status)
					}
				}
				complete := true
				for i := range checks {
					check := &checks[i]
					if check.found && !check.forbidden {
						continue
					}
					check.found, check.queryErr = lokiHasLogsSince(ctx, proxyClient.Services(releaseNamespace), check.expression, started)
					if check.forbidden {
						if check.queryErr != nil {
							return false, check.queryErr
						}
						if check.found {
							return false, fmt.Errorf("raw fixture credential reached Loki: %s", check.name)
						}
					} else {
						complete = complete && check.found && check.queryErr == nil
					}
				}
				if complete && observingSince.IsZero() {
					observingSince = time.Now()
					t.Logf("all host-file records arrived; checking redaction for another %s", observationWindow)
				}
				return complete && time.Since(observingSince) >= observationWindow, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			for _, check := range checks {
				if assert.NoError(t, check.queryErr, "querying %s", check.name) {
					if check.forbidden {
						assert.False(t, check.found, "%s: raw fixture credential reached Loki", check.name)
					} else {
						assert.True(t, check.found, "expected host-file record did not reach Loki: %s", check.expression)
					}
				}
			}
			assert.NoError(t, err, "polling host-file logs")
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

func hostFileFixturePod(node, marker, script string) *corev1.Pod {
	template := passiveFixturePodTemplate("nktp-e2e-hostfiles")
	template.Spec.NodeName = node
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.SecurityContext.RunAsNonRoot = new(false)
	template.Spec.Volumes = []corev1.Volume{{Name: "hostlogs", VolumeSource: corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{Path: "/var/log", Type: new(corev1.HostPathDirectory)},
	}}}
	container := &template.Spec.Containers[0]
	container.Name = "hostfiles"
	container.SecurityContext.RunAsUser = new(int64(0))
	container.VolumeMounts = []corev1.VolumeMount{{Name: "hostlogs", MountPath: "/hostlogs"}}
	container.Env = []corev1.EnvVar{{Name: "MARKER", Value: marker}}
	container.Command = []string{"/bin/sh", "-ec", script}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "nktp-e2e-hostfiles-", Labels: template.Labels}, Spec: template.Spec}
}

const hostFilesPreflightScript = `
for file in /hostlogs/auth.log /hostlogs/secure /hostlogs/audit/audit.log; do
  if [ -e "$file" ] || [ -L "$file" ]; then printf '%s\n' "$file"; fi
done
if [ -L /hostlogs/audit ]; then printf '%s\n' /hostlogs/audit; fi
`

const hostFilesWriterScript = `
if [ -L /hostlogs/audit ]; then echo "refusing a symlinked audit directory" >&2; exit 1; fi
for file in /hostlogs/auth.log /hostlogs/secure /hostlogs/audit/audit.log; do
  if [ -e "$file" ] || [ -L "$file" ]; then echo "refusing existing host file $file" >&2; exit 1; fi
done
if [ ! -d /hostlogs/audit ]; then
  mkdir /hostlogs/audit
  printf '%s\n' "$MARKER" > "/hostlogs/audit/.$MARKER"
fi
for pair in auth.log:authlog secure:secure audit/audit.log:audit; do
  file="/hostlogs/${pair%:*}"
  source="${pair#*:}"
  (set -C; printf '%s\n' "$MARKER-owner-$source" > "$file")
done
while true; do
  for pair in auth.log:authlog secure:secure audit/audit.log:audit; do
    file="/hostlogs/${pair%:*}"
    source="${pair#*:}"
    printf '%s\n' "$MARKER-$source plain" >> "$file"
    printf '%s\n' "$MARKER-$source credential password=nktp-e2e-fake-password Bearer nktp-e2e-fake-credential safe=kept" >> "$file"
  done
  sleep 2
done
`

const hostFilesCleanupScript = `
for pair in auth.log:authlog secure:secure audit/audit.log:audit; do
  file="/hostlogs/${pair%:*}"
  source="${pair#*:}"
  if [ ! -L "$file" ] && [ -f "$file" ] && [ "$(head -n 1 "$file")" = "$MARKER-owner-$source" ]; then
    rm "$file"
    test ! -e "$file"
  fi
done
if [ ! -L /hostlogs/audit ] && [ -f "/hostlogs/audit/.$MARKER" ]; then
  rm "/hostlogs/audit/.$MARKER"
  if [ -z "$(ls -A /hostlogs/audit)" ]; then rmdir /hostlogs/audit; fi
fi
`
