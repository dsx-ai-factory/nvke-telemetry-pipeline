# Getting started with NKTP

NKTP is one Helm chart that collects a Kubernetes cluster's metrics, logs and
traces and exports them over OTLP. This guide takes you through installation
readiness, viewing telemetry, and connecting a backend. The
[component pages](README.md#components) cover each component and its values.

## Install

Review the chart's [prerequisites](../nktp/README.md#prerequisites), then follow
its [installation instructions](../nktp/README.md#install).
To configure a backend or cluster name during installation, put the values
from the sections below in your overrides file.

The defaults require cert-manager and SSD-backed persistent storage. For
alternatives, see:

- [Install without cert-manager](guides/configure_cert_manager.md#installing-nktp-without-cert-manager).
- [Install without persistent storage](guides/configure_exporter_queue_storage.md#installing-nktp-without-persistent-storage).

## Check readiness

- **Wait for the Collectors too.** Helm 3's `--wait` does not wait for the
  Operator-managed Collector workloads. Run
  `kubectl get opentelemetrycollector -n nktp` and repeat until every
  Collector's `READY` column shows all expected replicas ready, such as `2/2`.
  If readiness stalls, inspect the Collector Pods with
  `kubectl get pods -n nktp`.
  When cert-manager is installed, also run `kubectl get certificate -n nktp`
  and wait for every NKTP Certificate to show `READY=True`; Helm 3's `--wait`
  does not check this. If certificate readiness stalls, inspect the conditions
  with `kubectl describe certificate -n nktp`.
- **Versions:** release tags and development builds are described in
  [RELEASING.md](../RELEASING.md).

<details>
<summary>Example Collector and certificate readiness with cert-manager installed</summary>

```bash
kubectl get opentelemetrycollector -A
kubectl get certificate -A
```

Example output for chart version `0.1.0`; names and replica counts depend on the
chart version and values. `READY=2/2` means both expected Collector replicas
are ready; certificates should show `READY=True`.

```text
NAMESPACE   NAME                  MODE          VERSION   READY   AGE   IMAGE                                      MANAGEMENT
nktp        nktp-cluster-events   statefulset   0.159.0   2/2     23h   otel/opentelemetry-collector-k8s:0.162.0   managed
...
```

```text
NAMESPACE   NAME                         READY   SECRET                          AGE
nktp        nktp-cluster-events-client   True    nktp-cluster-events-client-tls  23h
...
```

</details>

## First look

By default everything exports to a bundled development backend, `lgtm`, in the
release namespace: Grafana, Prometheus, Loki and Tempo in one Pod. It keeps data
in memory and Grafana grants anonymous admin access, so it is for evaluation
only.

Grafana is on port 3000 of the `lgtm` Service, with Prometheus, Loki and Tempo
as data sources for metrics, logs and traces. Port-forward it, bound to
localhost:

```bash
kubectl -n nktp port-forward svc/lgtm 3000:3000 --address 127.0.0.1
```

See [the default dashboards](../nktp/dashboards/README.md#default-autoloaded-into-lgtm)
for the dashboards that are included by default.

## Set a backend

Everything leaves the cluster through five Collectors: the three gateways, one
per signal, the watchdog, which carries the other Collectors' own telemetry,
and the watchdog's [sidecar](architecture/self_telemetry.md), which carries the
watchdog's own telemetry. Point all of them at your backend and turn off the
default LGTM backend. The default exporters use OTLP/gRPC. Check which protocol
your backend accepts for each signal; see [Secure gateway export](guides/secure_gateway_export.md)
for HTTP configuration. The metrics backend must also accept delta-temporality
metrics, which the Target Allocators send.

```yaml
otel-lgtm:
  enabled: false
watchdog:
  otelcol: {config: {exporters: {otlp: {endpoint: "<backend>:4317", tls: {insecure: false}}}}}
  sidecar:
    otelcol: {config: {exporters: {otlp: {endpoint: "<backend>:4317", tls: {insecure: false}}}}}
gateway_logs:
  otelcol: {config: {exporters: {otlp: {endpoint: "<backend>:4317", tls: {insecure: false}}}}}
gateway_metrics:
  otelcol: {config: {exporters: {otlp: {endpoint: "<backend>:4317", tls: {insecure: false}}}}}
gateway_traces:
  otelcol: {config: {exporters: {otlp: {endpoint: "<backend>:4317", tls: {insecure: false}}}}}
```

- **TLS and mTLS** to the backend: see [Secure gateway export](guides/secure_gateway_export.md).
- **Credentials** go in a Secret, never in values. Add it to the Collector's
  `kubernetes.env` and reference it from the exporter with `${env:NAME}`, for
  example in `headers`. `env` is a list, so restate the chart's `GOMEMLIMIT`
  and `GOGC` entries.

## Set the cluster name

Set `global.clusterName` so that telemetry from this cluster carries
`k8s.cluster.name`. Without it, NKTP sets `unknown`.

```yaml
global:
  clusterName: <cluster>
```

The gateways, the watchdog and the watchdog sidecar add the attribute to
telemetry that does not already have one.

## Service owners

See [Send telemetry to NKTP](guides/send_telemetry.md).

## Upgrading

Helm never upgrades CRDs it installed from a chart's `crds/` directory. The
OpenTelemetry and Prometheus CRDs come from there, so a chart version that bumps
the Operator leaves the old CRDs in place, and fields only the new version knows
are silently dropped. Apply the new CRDs before `helm upgrade`.

Changes to watchdog and gateway volume claim templates cause the Operator to
recreate their StatefulSets and restart Collectors. See
[Change the claim templates](guides/configure_exporter_queue_storage.md#change-the-claim-templates).
