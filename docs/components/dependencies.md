# Managed dependencies

NKTP installs these upstream charts as Helm dependencies. Their versions are
pinned in `nktp/Chart.yaml` and `nktp/Chart.lock`. Settings for each pass
through under its values key using the upstream chart's own values.

## Dependency update restrictions

[Dependabot's configuration](../../.github/dependabot.yml) distinguishes between
dependencies whose updates are limited to a compatible version series and images
whose versions are managed alongside another component. An owning component below
identifies the integration that determines compatibility, rather than a separate
team or an upstream maintenance commitment.

| Dependency | Owning component and pin location | Reason for the restriction | Review trigger |
| --- | --- | --- | --- |
| `registry.k8s.io/kubectl` | NKTP's OTel uninstall hook; `opentelemetry-kube-stack.cleanupJob.image.tag` in `nktp/values.yaml`. | Allow the 1.35 series. Kubernetes supports kubectl within one minor of the API server; 1.35 fits both ends of the current 1.34/1.36 e2e matrix. | When changing the tested Kubernetes range, review the hook image and Dependabot range together, and verify uninstall on each tested version. |
| `prometheus-node-exporter` | NKTP's node-exporter integration; the direct chart dependency in `nktp/Chart.yaml` and `nktp/Chart.lock`. | Allow chart 4.x updates while the 5.x security-default changes are reviewed, including dropped capabilities, disabled privilege escalation, and RuntimeDefault seccomp for containers and sidecars. This is a chart-version restriction, not an exporter-image version. | When adopting chart 5.x, review upstream migration guidance and configured security overrides, verify scraping, and update the range alongside the chart pin. |
| `ghcr.io/open-telemetry/opentelemetry-operator/target-allocator` | The Operator bundled by `opentelemetry-kube-stack`; the empty `targetallocator_image.tag` in `nktp/values.yaml` selects that Operator's version. | Ignore all independent image updates so Dependabot does not write a separate tag. | On every kube-stack upgrade, review any workload-specific `kubernetes.targetAllocator.image` overrides and verify target discovery and mTLS. |
| `github.com/open-telemetry/opentelemetry-operator/apis` | The Operator bundled by `opentelemetry-kube-stack`; `tests/go.mod`. | Ignore all independent module updates; the e2e tests must use the CRD types of the Operator the chart installs. | On every kube-stack upgrade, update the module to the bundled Operator version. The integration tests fail until they match. |

The kubectl range reflects the current e2e matrix; it does not establish support
for every Kubernetes version mentioned elsewhere in the documentation. See the
[Kubernetes version-skew policy](https://kubernetes.io/releases/version-skew-policy/#kubectl)
and the [node-exporter chart 5.x migration guide](https://github.com/prometheus-community/helm-charts/blob/prometheus-node-exporter-5.0.0/charts/prometheus-node-exporter/README.md#4x-to-5x).

When adding or changing an ignore rule, keep its rationale, owning component, pin
location, and review trigger together here and link to this section from the
configuration. Version ranges continue to permit updates inside the range; full
ignores require an explicit update with the owning component. Neither replaces
compatibility testing. A restriction on a direct chart also does not constrain
the versions bundled inside other upstream charts.

## OpenTelemetry Operator and CRDs

NKTP depends on the published [opentelemetry-kube-stack](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/main/charts/opentelemetry-kube-stack)
chart, which supplies the OpenTelemetry Operator and native CRDs.
NKTP disables its default Collector and general Collector RBAC.
Settings pass through under `opentelemetry-kube-stack` using upstream values.

Helm installs missing OpenTelemetry CRDs (OpenTelemetryCollector, Instrumentation,
TargetAllocator, OpAMPBridge) and Prometheus CRDs (ServiceMonitor, PodMonitor, Probe,
ScrapeConfig) before the chart's resources.

The kube-stack chart also supplies the Kubernetes component ServiceMonitors that
the large-target pool scrapes. See
[Kubernetes component metrics](large_target_metrics.md#kubernetes-component-metrics).

The minimum supported API version for `OpenTelemetryCollector` resources is `opentelemetry.io/v1beta1`.

Use these independent settings when another mechanism manages the Operator or CRDs.

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    enabled: false
  crds:
    installOtel: false
    installPrometheus: false
```

Override the Operator image with
`opentelemetry-kube-stack.opentelemetry-operator.manager.image.tag`.

The Operator's admission webhook certificate comes from cert-manager by default.
See [Configure cert-manager](../guides/configure_cert_manager.md#installing-nktp-without-cert-manager) for the
alternative.

### Collector image pull secrets

Set `global.imagePullSecrets: [{name: registry-credentials}]` to share existing
registry Secrets in the release namespace across NKTP Collectors, their sidecars,
and Target Allocators.
Override `<component>.kubernetes.imagePullSecrets` to replace the list, or use
`[]` to opt out of the chart's shared default.

## OpenTelemetry version alignment

OpenTelemetry components that NKTP installs follow three release lines. Each
line has one source in the chart.

| Release line | Source | Follows it |
| --- | --- | --- |
| Collector | `otelcol_image` in `nktp/values.yaml` | Every chart workload, and dev profiles, which set only `repository` |
| Operator | The kube-stack version in `nktp/Chart.lock` | The Target Allocator, through an empty `targetallocator_image.tag`, and the Operator API module in `tests/go.mod` |
| `grafana/otel-lgtm` | `otel-lgtm.image` in `nktp/values.yaml` | The Collector it embeds, which its release notes name |

The lines are released separately upstream, so their versions differ. The
Operator and the Target Allocator are released together and always match.
The scale suite under `tests/scale` pins its own test images and is updated
separately.

Links to upstream source and documentation also name a version; update them
with the pin they describe, or link to a version whose content still applies.

## Node exporter

NKTP installs [prometheus-node-exporter](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-node-exporter) by default
to collect host-level metrics from every node. It runs as a DaemonSet with host networking and binds port 9100 on each node,
and ships a ServiceMonitor so the exporter is discoverable as a scrape target.
Its DaemonSet tolerates all taints, including `NoExecute`, matching the node Collector
and KSM DaemonSet. Override `node-exporter.tolerations` to restrict this.

Disable it, for example when the cluster already runs a node exporter:

```yaml
node-exporter:
  enabled: false
```

NKTP sets `node-exporter.image.tag` to `v1.13.1`. The chart's default, node_exporter 1.12.1, hangs in its
cpufreq collector on arm64 nodes that use the `cppc_cpufreq` driver, so scrapes of the exporter time out
([prometheus/node_exporter#3791](https://github.com/prometheus/node_exporter/issues/3791)). If you override
`node-exporter.image`, for example to pull from a mirror, use node_exporter 1.13.1 or later.

NKTP does not support node_exporter's own TLS server or its supervisord collector. The chart's values schema
rejects `--web.config.file` and `--collector.supervisord` in `node-exporter.extraArgs`, and also rejects
entries that start with `@` (an arguments file) or contain `$(` (a Kubernetes variable reference), because
either could supply those flags. Helm reports `at '/node-exporter/extraArgs/<n>': 'not' failed` for the
rejected entry. The chart's templates apply the same check, so it also holds with `--skip-schema-validation`.
To serve the exporter's metrics over
TLS, use the subchart's `kubeRBACProxy` and `tlsSecret` values, which put a kube-rbac-proxy sidecar in front of
the exporter. The ServiceMonitor must then scrape over HTTPS (`node-exporter.prometheus.monitor.scheme: https`
and `node-exporter.prometheus.monitor.tlsConfig` to trust the certificate), and the scraper must send a bearer
token (`node-exporter.prometheus.monitor.bearerTokenFile`) whose identity Kubernetes RBAC allows to read the metrics, because
kube-rbac-proxy authorizes each request.

## Blackbox exporter

[prometheus-blackbox-exporter](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-blackbox-exporter)
probes endpoints over HTTP, TCP, DNS, gRPC, and ICMP, and returns the result as
metrics. NKTP does **not** install it by default. Enable it to probe endpoints
declared with Probe resources:

```yaml
blackbox-exporter:
  enabled: true
```

The bundled chart is pinned to 11.20.2, which installs blackbox_exporter 0.29.1.
NKTP names it `nktp-blackbox-exporter`, runs 2 replicas with `RuntimeDefault`
seccomp and small resource limits, and enables its self-monitoring
ServiceMonitor. Its image reads `global.imageRegistry`. Other settings pass
through under `blackbox-exporter` using the upstream chart's values. See
[Blackbox probing](nodeless_metrics.md#blackbox-probing) for how Probes reach the
nodeless metrics pool.

## Metrics server

[metrics-server](https://github.com/kubernetes-sigs/metrics-server) serves the Kubernetes
resource metrics API (`metrics.k8s.io`), which `kubectl top` and Horizontal Pod Autoscalers
read. It is what drives horizontal autoscaling of the three signal gateways, the
large-target metrics pool, and the watchdog: every workload with an `autoscaler` block
scales on CPU and memory utilization, which the HPA controller can only read from this API.

NKTP does **not** install it by default, because many clusters already get it from a
managed control plane or a cluster add-on, and a second copy conflicts over the
cluster-singleton `v1beta1.metrics.k8s.io` APIService. With no provider at all, those HPAs
cannot compute a recommendation and stop scaling, holding each workload at whatever
replica count it currently has. Enable it when nothing else supplies the API:

```yaml
metrics-server:
  enabled: true
```

See the [bundled dependency and image inventory](../../nktp/README.md#bundled-dependencies)
for the current chart and image versions. Enabling the bundled metrics-server
requires Kubernetes 1.34+; see the [upstream compatibility matrix](https://github.com/kubernetes-sigs/metrics-server/blob/v0.9.0/README.md#compatibility-matrix).

Settings pass through under `metrics-server` using the upstream chart's own values, none
of which NKTP overrides. See the [chart's values reference](https://github.com/kubernetes-sigs/metrics-server/blob/metrics-server-helm-chart-3.14.0/charts/metrics-server/README.md)
for what is configurable, including the TLS defaults for the APIService and for kubelet
certificate verification.

By default, metrics-server verifies each kubelet's serving certificate. The certificate
must be signed by a CA that metrics-server trusts and include a subject alternative
name (SAN) matching the node address metrics-server uses to reach the kubelet. For
production, provision valid kubelet serving certificates and configure metrics-server
to trust their issuing CA, using `--kubelet-certificate-authority` when needed. Keep
kubelet certificate verification enabled.

On clusters with self-signed or otherwise unverifiable kubelet certificates, including
typical Kind clusters, metrics-server can fail to scrape nodes and remain unready.
Its logs show `x509` certificate verification errors, and `helm install --wait` can
time out waiting for the metrics-server Deployment to become ready. Increasing Helm's
timeout does not fix certificate trust.

For local testing on Kind only, add this override to your installation values:

```yaml
metrics-server:
  enabled: true
  args:
    - --kubelet-insecure-tls
```

`--kubelet-insecure-tls` disables verification of kubelet serving certificates. Do not
use it in production. This testing override leaves NKTP's defaults unchanged and is
separate from `metrics-server.apiService.insecureSkipTLSVerify`, which controls the
Kubernetes API server's verification of metrics-server's own serving certificate.
