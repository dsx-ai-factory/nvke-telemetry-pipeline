# Managed dependencies

NKTP installs these upstream charts as Helm dependencies. Their versions are
pinned in `nktp/Chart.yaml` and `nktp/Chart.lock`. Settings for each pass
through under its values key using the upstream chart's own values.

## OpenTelemetry Operator and CRDs

NKTP depends on the published [opentelemetry-kube-stack 0.25.1](https://github.com/open-telemetry/opentelemetry-helm-charts/tree/opentelemetry-kube-stack-0.25.1/charts/opentelemetry-kube-stack)
chart. It supplies OpenTelemetry Operator chart 0.126.0, operator image 0.161.0,
and native CRDs. NKTP disables its default Collector and general Collector RBAC.
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

The exporter's cpufreq collector is turned off with `--no-collector.cpufreq`. In node_exporter 1.12.1,
this collector hangs on arm64 nodes that use the `cppc_cpufreq` driver, so scrapes of the exporter time out
([prometheus/node_exporter#3791](https://github.com/prometheus/node_exporter/issues/3791)). As a result,
the exporter does not produce the `node_cpu_frequency_*` and `node_cpu_scaling_*` series.
To turn the collector back on, set `extraArgs` to an empty list:

```yaml
node-exporter:
  extraArgs: []
```

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

The bundled chart is pinned to metrics-server chart 3.13.1, which installs metrics-server
0.8.1. That version supports Kubernetes 1.31+.

Settings pass through under `metrics-server` using the upstream chart's own values, none
of which NKTP overrides. See the [chart's values reference](https://github.com/kubernetes-sigs/metrics-server/blob/metrics-server-helm-chart-3.13.1/charts/metrics-server/README.md)
for what is configurable, including the TLS defaults for the APIService and for kubelet
certificate verification.
