// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	workload "github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const componentLogsFixtureName = "nktp-e2e-component-logs"

// TestKubernetesComponentLogs generates component activity and checks that its
// logs and API audit events reach Loki with the expected Kubernetes metadata.
// It requires audit logging and component verbosity from the e2e Kind config,
// plus pods/proxy access to etcd's TLS port. CoreDNS query logging is temporarily
// enabled. Cleanup restores CoreDNS and removes the Service and workload fixtures.
func TestKubernetesComponentLogs(t *testing.T) {
	namespace := testenv.EnvConf().Namespace()
	var client klient.Client
	var proxyClient coreclient.CoreV1Interface
	var etcdPods corev1.PodList
	var started time.Time

	f := features.New("Kubernetes component logs").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			setupCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			started = time.Now()
			var err error
			client, err = c.NewClient()
			require.NoError(t, err, "creating the Kubernetes client")
			// Proxy and pod-log requests are not exposed by the framework's CRUD client.
			proxyClient, err = coreclient.NewForConfig(client.RESTConfig())
			require.NoError(t, err, "creating the proxy and pod-log client")
			require.NoError(t, client.Resources("kube-system").List(setupCtx, &etcdPods,
				resources.WithLabelSelector("component=etcd")), "listing etcd pods for the TLS rejection probe")
			require.NotEmpty(t, etcdPods.Items, "the cluster must have an etcd pod to test")
			createCoreDNSLogsFixture(setupCtx, t, client)
			createComponentLogsFixture(setupCtx, t, client, namespace)
			return ctx
		})

	for _, name := range []string{
		"kube-apiserver-audit", "kube-apiserver-pod", "etcd-pod",
		"kube-scheduler-pod", "kube-controller-manager-pod", "coredns-pod", "kube-proxy-pod",
	} {
		// Loki normalizes dots to underscores. Pipeline filters also work
		// for attributes stored as structured metadata rather than labels.
		var expression string
		if name == "kube-apiserver-audit" {
			expression = `{service_name="kube-apiserver"} | nvidia_telemetry_class="k8s.api.audit" | json audit_kind="kind", audit_api_version="apiVersion" | audit_kind="Event" | audit_api_version="audit.k8s.io/v1"`
		} else {
			component := strings.TrimSuffix(name, "-pod")
			expression = fmt.Sprintf(`{k8s_namespace_name="kube-system"} | k8s_container_name=%q | k8s_pod_name=~%q`, component, component+"-.+")
			// CoreDNS and kube-proxy retain their Kubernetes attributes but
			// are not assigned k8s.control_plane by the chart's classification rule.
			if component != "coredns" && component != "kube-proxy" {
				expression += ` | nvidia_telemetry_class="k8s.control_plane"`
			}
		}
		// Require the controlled stimuli, rather than unrelated startup messages.
		switch name {
		case "kube-apiserver-pod":
			expression += fmt.Sprintf(` |= "HTTP" |= %q`, "/api/v1/namespaces/"+namespace)
		case "kube-scheduler-pod":
			expression += fmt.Sprintf(` |= "Successfully bound pod to node" |= %q`, componentLogsFixtureName)
		case "kube-controller-manager-pod":
			expression += fmt.Sprintf(` |= "Controller created pod" |= %q`, componentLogsFixtureName)
		case "etcd-pod":
			expression += ` |= "rejected connection on client endpoint"`
		case "coredns-pod":
			expression += fmt.Sprintf(` |= %q`, componentLogsFixtureName+"."+namespace+".svc.cluster.local.")
		case "kube-proxy-pod":
			expression += fmt.Sprintf(` |= "Service updated ports" |= %q`, namespace+"/"+componentLogsFixtureName)
		}

		f.Assess(name, func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			r := client.Resources(namespace)
			lastRefresh := time.Now()
			var lastEtcdProbe time.Time
			var found bool
			var queryErr error
			err := wait.For(func(ctx context.Context) (bool, error) {
				// Generate fresh audit events after file_log starts at EOF.
				var ns corev1.Namespace
				if err := r.Get(ctx, namespace, "", &ns); err != nil {
					t.Logf("reading the component logs Namespace: %v", err)
				}

				// Retry stimuli at most every 30 seconds in case the first
				// messages were written before file_log began reading.
				switch name {
				case "kube-scheduler-pod", "kube-controller-manager-pod":
					if time.Since(lastRefresh) >= 30*time.Second {
						lastRefresh = time.Now()
						var pods corev1.PodList
						if err := r.List(ctx, &pods, resources.WithLabelSelector("app.kubernetes.io/name="+componentLogsFixtureName)); err != nil {
							t.Logf("listing the component logs fixture pods: %v", err)
						} else {
							for _, pod := range pods.Items {
								if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
									t.Logf("refreshing the component logs fixture: %v", err)
								}
							}
						}
					}
				case "etcd-pod":
					if time.Since(lastEtcdProbe) >= 30*time.Second {
						lastEtcdProbe = time.Now()
						for _, pod := range etcdPods.Items {
							probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
							// HTTP cannot handshake with etcd's TLS endpoint. The
							// rejection log, not the proxy error, proves success.
							_, _ = proxyClient.Pods("kube-system").ProxyGet("http", pod.Name, "2379", "/version", nil).DoRaw(probeCtx)
							probeCancel()
						}
					}
				case "kube-proxy-pod":
					if time.Since(lastRefresh) >= 30*time.Second {
						lastRefresh = time.Now()
						var service corev1.Service
						if err := r.Get(ctx, componentLogsFixtureName, namespace, &service); err != nil {
							t.Logf("reading the component logs Service: %v", err)
						} else if len(service.Spec.Ports) == 0 {
							t.Log("component logs Service has no ports to refresh")
						} else {
							// Metadata-only updates do not produce port-change logs.
							if service.Spec.Ports[0].Port == 8080 {
								service.Spec.Ports[0].Port = 8081
							} else {
								service.Spec.Ports[0].Port = 8080
							}
							if err := r.Update(ctx, &service); err != nil {
								t.Logf("refreshing the component logs Service: %v", err)
							}
						}
					}
				}
				found, queryErr = lokiHasLogsSince(ctx, proxyClient.Services(namespace), expression, started)
				return queryErr == nil && found, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			if err != nil {
				t.Logf("%s log polling stopped: %v", name, err)
				logComponentLogsDiagnostics(context.WithoutCancel(ctx), t, client, proxyClient, namespace, name, expression, started)
			}
			if assert.NoError(t, queryErr, "querying %s logs in LGTM", name) {
				assert.True(t, found, "no %s log in LGTM since %s matching %s",
					name, started.Format(time.RFC3339), expression)
			}
			assert.NoError(t, err, "polling %s logs", name)
			return ctx
		})
	}
	testenv.Test(t, f.Feature())
}

// logComponentLogsDiagnostics summarizes source activity and ingestion failures.
func logComponentLogsDiagnostics(ctx context.Context, t *testing.T, client klient.Client, proxyClient coreclient.CoreV1Interface, namespace, name, expression string, started time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// Only the two collectors on this path and LGTM's internal collector matter.
	probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
	metrics, err := proxyClient.Services(namespace).ProxyGet("http", "lgtm", "9090", "/api/v1/query", map[string]string{
		"query": `sum by (__name__, service_name, receiver, exporter) ({__name__=~"otelcol_(receiver_(accepted|refused)_log_records|exporter_(sent|send_failed)_log_records)(_total)?",service_name=~"nktp-daemonset|nktp-gateway-logs|otelcol-contrib"})`,
	}).DoRaw(probeCtx)
	probeCancel()
	var counters struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err == nil {
		err = json.Unmarshal(metrics, &counters)
	}
	if err != nil || counters.Status != "success" {
		t.Logf("log diagnostics: collector counters unavailable: error=%v status=%s detail=%s", err, counters.Status, counters.Error)
	} else {
		for _, service := range []string{"nktp-daemonset", "nktp-gateway-logs", "otelcol-contrib"} {
			summary := map[string]string{}
			for _, result := range counters.Data.Result {
				if result.Metric["service_name"] != service {
					continue
				}
				component := result.Metric["receiver"]
				if component == "" {
					component = result.Metric["exporter"]
				}
				if component != "file_log" && component != "file_log/kube-api-audit" && component != "otlp" && component != "otlp_grpc" && component != "otlp_http" && component != "otlp_http/logs" {
					continue
				}
				name := strings.NewReplacer(
					"otelcol_receiver_accepted_log_records", "accepted",
					"otelcol_receiver_refused_log_records", "refused",
					"otelcol_exporter_sent_log_records", "sent",
					"otelcol_exporter_send_failed_log_records", "failed",
				).Replace(strings.TrimSuffix(result.Metric["__name__"], "_total"))
				var value string
				if json.Unmarshal(result.Value[1], &value) == nil {
					summary[component+":"+name] = value
				}
			}
			t.Logf("log diagnostics: cumulative counters %s %v", service, summary)
		}
	}

	// LGTM's collector can accept OTLP while Loki rejects every export. Its
	// own /ready and rejection counters expose that otherwise hidden final hop.
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	var backend corev1.PodList
	err = client.Resources(namespace).List(probeCtx, &backend, resources.WithLabelSelector("app=lgtm"))
	probeCancel()
	if err != nil || len(backend.Items) != 1 {
		t.Logf("log diagnostics: locating LGTM: error=%v", err)
	} else {
		pod := backend.Items[0]
		logLGTMIngestionDiagnostics(ctx, t, proxyClient, pod)
		probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
		ready, readyErr := proxyClient.Pods(namespace).ProxyGet("http", pod.Name, "3100", "/ready", nil).DoRaw(probeCtx)
		probeCancel()
		t.Logf("log diagnostics: Loki ready=%t error=%v", strings.TrimSpace(string(ready)) == "ready", readyErr)
		probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
		body, metricsErr := proxyClient.Pods(namespace).ProxyGet("http", pod.Name, "3100", "/metrics", nil).DoRaw(probeCtx)
		probeCancel()
		// Select only numeric values and known operational labels. Never emit
		// arbitrary stream labels, which can contain application attributes.
		summary := map[string]float64{}
		label := regexp.MustCompile(`(?:reason|status_code)="([^"]+)"`)
		ringState := regexp.MustCompile(`state="([^"]+)"`)
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value, parseErr := strconv.ParseFloat(fields[1], 64)
			if parseErr != nil {
				continue
			}
			switch {
			case strings.HasPrefix(line, "loki_discarded_samples_total{"):
				if match := label.FindStringSubmatch(line); len(match) == 2 {
					summary["rejected:"+match[1]] += value
				}
			case strings.HasPrefix(line, "loki_request_duration_seconds_count{") && strings.Contains(line, `route="otlp_v1_logs"`):
				if match := label.FindStringSubmatch(line); len(match) == 2 {
					summary["otlp_http:"+match[1]] += value
				}
			case strings.HasPrefix(line, "loki_distributor_lines_received_total{"):
				summary["received"] += value
			case strings.HasPrefix(line, "loki_ingester_memory_streams{"):
				summary["streams"] += value
			// Loki 3.7 can report disk-throttled pushes as "Ingester is
			// shutting down" while /ready and /services remain healthy.
			case fields[0] == "loki_ingester_wal_disk_usage_percent":
				summary["wal_disk_usage_percent"] = value * 100
			case fields[0] == "loki_ingester_wal_disk_full_failures_total":
				summary["wal_disk_full_failures"] = value
			case strings.HasPrefix(line, "loki_distributor_ingester_appends_failures_total"):
				summary["ingester_append_failures"] += value
			case strings.HasPrefix(line, "cortex_ring_members{") && strings.Contains(line, `name="ingester"`):
				if match := ringState.FindStringSubmatch(line); len(match) == 2 {
					summary["ingester_ring:"+match[1]] = value
				}
			}
		}
		t.Logf("log diagnostics: Loki cumulative ingestion %v error=%v", summary, metricsErr)
	}

	// Include index labels and a broad selector to expose an ingestion mapping
	// mismatch without copying log payloads.
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	labels, labelsErr := proxyClient.Services(namespace).ProxyGet("http", "lgtm", "3000",
		"/api/datasources/proxy/uid/loki/loki/api/v1/labels", map[string]string{
			"start": fmt.Sprint(started.UnixNano()), "end": fmt.Sprint(time.Now().UnixNano()),
		}).DoRaw(probeCtx)
	probeCancel()
	var labelResponse struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if labelsErr == nil {
		labelsErr = json.Unmarshal(labels, &labelResponse)
	}
	t.Logf("log diagnostics: Loki index labels since test start=%v status=%s error=%v", labelResponse.Data, labelResponse.Status, labelsErr)
	selector, _, _ := strings.Cut(expression, "}")
	selector += "}"
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	present, err := lokiHasLogsSince(probeCtx, proxyClient.Services(namespace), selector, started)
	probeCancel()
	t.Logf("log diagnostics: Loki selector=%s present=%t error=%v", selector, present, err)

	// Count the controlled messages at their source rather than dumping payloads.
	if !strings.HasSuffix(name, "-pod") || ctx.Err() != nil {
		return
	}
	probeCtx, probeCancel = context.WithTimeout(ctx, 3*time.Second)
	var pods corev1.PodList
	err = client.Resources("kube-system").List(probeCtx, &pods)
	probeCancel()
	if err != nil {
		t.Logf("log diagnostics: source pods unavailable: %v", err)
		return
	}
	sources := map[string]int{}
	component := strings.TrimSuffix(name, "-pod")
	tokens := strings.Split(expression, " |= ")[1:]
	for _, pod := range pods.Items {
		if !strings.HasPrefix(pod.Name, component+"-") {
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		body, err := proxyClient.Pods("kube-system").GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: component, SinceTime: &metav1.Time{Time: started},
			TailLines: new(int64(200)), LimitBytes: new(int64(65536)),
		}).DoRaw(probeCtx)
		probeCancel()
		if err != nil {
			t.Logf("log diagnostics: reading source %s: %v", pod.Name, err)
			continue
		}
		// Preserve a zero entry to distinguish no matches from no pod.
		sources[component] += 0
		for _, line := range strings.Split(string(body), "\n") {
			matches := len(tokens) > 0
			for _, token := range tokens {
				text, err := strconv.Unquote(token)
				matches = matches && err == nil && strings.Contains(line, text)
			}
			if matches {
				sources[component]++
			}
		}
	}
	t.Logf("log diagnostics: matching messages in Kubernetes pod logs since test start=%v (not Loki; last 200 lines/64KiB per pod)", sources)
}

// logLGTMIngestionDiagnostics reports backend health and recent operational errors.
func logLGTMIngestionDiagnostics(ctx context.Context, t *testing.T, client coreclient.CoreV1Interface, pod corev1.Pod) {
	t.Helper()
	restarted := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "lgtm" {
			continue
		}
		restarted = status.RestartCount > 0
		t.Logf("log diagnostics: LGTM ready=%t restarts=%d", status.Ready, status.RestartCount)
		if terminated := status.LastTerminationState.Terminated; terminated != nil {
			t.Logf("log diagnostics: LGTM previous exit=%d reason=%s finished=%s", terminated.ExitCode, terminated.Reason, terminated.FinishedAt.Format(time.RFC3339))
		}
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
	services, err := client.Pods(pod.Namespace).ProxyGet("http", pod.Name, "3100", "/services", nil).DoRaw(probeCtx)
	probeCancel()
	// /services may be HTML. Retain only its text and bound the summary.
	text := strings.Join(strings.Fields(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(string(services), " ")), " ")
	if len(text) > 2048 {
		text = text[:2048] + " (truncated)"
	}
	t.Logf("log diagnostics: Loki services=%q error=%v", text, err)

	severity := regexp.MustCompile(`(?i)(\blevel=(warn|error)\b|\t(WARN|ERROR)\t|"level"\s*:\s*"(warn|error)")`)
	for _, previous := range []bool{false, true} {
		if previous && !restarted {
			continue
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		body, err := client.Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: "lgtm", Previous: previous,
			TailLines: new(int64(400)), LimitBytes: new(int64(65536)),
		}).DoRaw(probeCtx)
		probeCancel()
		var messages []string
		for _, line := range strings.Split(string(body), "\n") {
			if severity.MatchString(line) {
				if len(line) > 2048 {
					line = line[:2048] + " (truncated)"
				}
				messages = append(messages, line)
			}
		}
		t.Logf("log diagnostics: LGTM previous=%t warnings/errors=%d (last 400 lines/64KiB) error=%v", previous, len(messages), err)
		for _, message := range messages[max(0, len(messages)-10):] {
			t.Logf("log diagnostics: LGTM previous=%t %s", previous, message)
		}
	}
}

// createCoreDNSLogsFixture preserves the cluster's DNS configuration and only
// adds query logging. A separate ConfigMap lets cleanup delete everything this
// test creates without deleting or overwriting the cluster-owned ConfigMap.
func createCoreDNSLogsFixture(ctx context.Context, t *testing.T, client klient.Client) {
	t.Helper()
	r := client.Resources("kube-system")
	var deployment appsv1.Deployment
	require.NoError(t, r.Get(ctx, "coredns", "kube-system", &deployment), "reading the CoreDNS Deployment")
	var volumeName string
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil && volume.ConfigMap.Name == "coredns" {
			volumeName = volume.Name
			break
		}
	}
	require.NotEmpty(t, volumeName, "CoreDNS must mount the coredns ConfigMap")
	var original corev1.ConfigMap
	require.NoError(t, r.Get(ctx, "coredns", "kube-system", &original), "reading the CoreDNS ConfigMap")
	lines := strings.Split(original.Data["Corefile"], "\n")
	configured := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "log" {
			configured = true
			break
		}
	}
	if !configured {
		for i, line := range lines {
			if strings.TrimSpace(line) == "errors" {
				lines[i] = line + "\n    log"
				configured = true
				break
			}
		}
	}
	require.True(t, configured, "CoreDNS Corefile must have an errors or log directive")
	config := original.DeepCopy()
	fixture := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{GenerateName: componentLogsFixtureName + "-coredns-", Namespace: "kube-system"},
		Data:       config.Data,
		BinaryData: config.BinaryData,
	}
	fixture.Data["Corefile"] = strings.Join(lines, "\n")
	require.NoError(t, r.Create(ctx, fixture), "creating the CoreDNS logs ConfigMap")
	rolloutOpts := workload.DeploymentWaitOptions{
		Name: "coredns", Namespace: "kube-system", Client: client, Rollout: true,
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute+30*time.Second)
		defer cancel()
		if !assert.NoError(t, setCoreDNSConfigMap(cleanupCtx, client, volumeName, fixture.Name, original.Name), "restoring CoreDNS configuration") {
			return
		}
		// Keep the temporary ConfigMap until the original configuration is
		// serving again, so an incomplete rollout cannot strand DNS pods.
		if !assert.NoError(t, workload.WaitForDeployment(cleanupCtx, rolloutOpts), "waiting for restored CoreDNS") {
			return
		}
		err := client.Resources("kube-system").Delete(cleanupCtx, fixture, func(opts *metav1.DeleteOptions) {
			opts.Preconditions = &metav1.Preconditions{UID: &fixture.UID}
		})
		if !apierrors.IsNotFound(err) {
			assert.NoError(t, err, "deleting the CoreDNS logs ConfigMap")
		}
	})
	require.NoError(t, setCoreDNSConfigMap(ctx, client, volumeName, original.Name, fixture.Name), "enabling CoreDNS query logs")
	require.NoError(t, workload.WaitForDeployment(ctx, rolloutOpts), "waiting for CoreDNS query logging")
}

func setCoreDNSConfigMap(ctx context.Context, client klient.Client, volumeName, from, to string) error {
	r := client.Resources("kube-system")
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var deployment appsv1.Deployment
		if err := r.Get(ctx, "coredns", "kube-system", &deployment); err != nil {
			return err
		}
		for i := range deployment.Spec.Template.Spec.Volumes {
			volume := &deployment.Spec.Template.Spec.Volumes[i]
			if volume.Name != volumeName || volume.ConfigMap == nil {
				continue
			}
			if volume.ConfigMap.Name == to {
				return nil
			}
			if volume.ConfigMap.Name != from {
				return fmt.Errorf("CoreDNS volume %s now references %s, expected %s", volumeName, volume.ConfigMap.Name, from)
			}
			volume.ConfigMap.Name = to
			return r.Update(ctx, &deployment)
		}
		return fmt.Errorf("CoreDNS ConfigMap volume %s is missing", volumeName)
	})
}

func createComponentLogsFixture(ctx context.Context, t *testing.T, client klient.Client, namespace string) {
	t.Helper()
	r := client.Resources(namespace)
	fixture := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: componentLogsFixtureName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": componentLogsFixtureName}},
			Template: passiveFixturePodTemplate(componentLogsFixtureName),
		},
	}
	// Use the existing Alpine fixture's BusyBox rather than another image.
	// Kind's cluster domain is cluster.local. The trailing dot prevents DNS
	// search-path expansion, and repeated queries cover file_log's EOF startup.
	fixture.Spec.Template.Spec.Containers[0].Command = []string{
		"/bin/sh", "-ec",
		`while true; do /bin/busybox nslookup "$FIXTURE_DNS_NAME" >/dev/null 2>&1 || true; sleep 5; done`,
	}
	fixture.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{
		Name: "FIXTURE_DNS_NAME", Value: componentLogsFixtureName + "." + namespace + ".svc.cluster.local.",
	}}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: componentLogsFixtureName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": componentLogsFixtureName},
			Ports:    []corev1.ServicePort{{Name: "fixture", Port: 8080}},
		},
	}
	require.NoError(t, r.Create(ctx, fixture), "creating the component logs fixture")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute+30*time.Second)
		defer cancel()
		assert.NoError(t, deleteComponentLogsDeployment(cleanupCtx, client, fixture), "removing the component logs Deployment")
	})
	require.NoError(t, r.Create(ctx, service), "creating the component logs Service")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		assert.NoError(t, deleteComponentLogsService(cleanupCtx, client, service), "removing the component logs Service")
	})
	require.NoError(t, workload.WaitForDeployment(ctx, workload.DeploymentWaitOptions{
		Name: fixture.Name, Namespace: namespace, Client: client,
		Rollout: true, ReplicaCount: new(int32(1)),
	}), "waiting for the component logs fixture")
}

func deleteComponentLogsService(ctx context.Context, client klient.Client, service *corev1.Service) error {
	err := client.Resources(service.Namespace).Delete(ctx, service, func(opts *metav1.DeleteOptions) {
		opts.Preconditions = &metav1.Preconditions{UID: &service.UID}
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func deleteComponentLogsDeployment(ctx context.Context, client klient.Client, deployment *appsv1.Deployment) error {
	r := client.Resources(deployment.Namespace)
	err := r.Delete(ctx, deployment, func(opts *metav1.DeleteOptions) {
		opts.PropagationPolicy = new(metav1.DeletePropagationForeground)
		opts.Preconditions = &metav1.Preconditions{UID: &deployment.UID}
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	selector := resources.WithLabelSelector("app.kubernetes.io/name=" + componentLogsFixtureName)
	return wait.For(func(ctx context.Context) (bool, error) {
		var current appsv1.Deployment
		err := r.Get(ctx, deployment.Name, deployment.Namespace, &current)
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		var pods corev1.PodList
		if listErr := r.List(ctx, &pods, selector); listErr != nil {
			return false, listErr
		}
		var replicaSets appsv1.ReplicaSetList
		if listErr := r.List(ctx, &replicaSets, selector); listErr != nil {
			return false, listErr
		}
		return apierrors.IsNotFound(err) && len(pods.Items) == 0 && len(replicaSets.Items) == 0, nil
	}, wait.WithContext(ctx), wait.WithTimeout(0), wait.WithInterval(2*time.Second), wait.WithImmediate())
}

func lokiHasLogsSince(ctx context.Context, services coreclient.ServiceInterface, expression string, started time.Time) (bool, error) {
	// LGTM exposes Grafana, whose provisioned Loki datasource proxies the
	// internal Loki endpoint without requiring another chart service port.
	now := time.Now()
	body, err := services.ProxyGet("http", "lgtm", "3000",
		"/api/datasources/proxy/uid/loki/loki/api/v1/query_range", map[string]string{
			"query": expression,
			"start": fmt.Sprint(started.UnixNano()),
			"end":   fmt.Sprint(now.UnixNano()),
			"limit": "1",
		}).DoRaw(ctx)
	if err != nil {
		return false, fmt.Errorf("querying Loki for %q: %w", expression, err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false, fmt.Errorf("decoding Loki response for %q: %w", expression, err)
	}
	if response.Status != "success" {
		return false, fmt.Errorf("Loki query %q returned status %q: %s", expression, response.Status, response.Error)
	}
	if response.Data.ResultType != "streams" {
		return false, fmt.Errorf("Loki query %q returned result type %q, want streams", expression, response.Data.ResultType)
	}
	for _, result := range response.Data.Result {
		if len(result.Values) > 0 {
			return true, nil
		}
	}
	return false, nil
}
