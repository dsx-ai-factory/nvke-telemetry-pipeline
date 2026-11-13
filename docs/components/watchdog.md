# Watchdog

The watchdog receives internal metrics, logs and traces from NKTP collectors over
OTLP/gRPC with mTLS at `nktp-watchdog-collector:4317` in the Helm release namespace.
Target Allocators send their own metrics over plaintext OTLP/gRPC at
`nktp-watchdog-collector:4320`; that receiver is used only by the metrics pipeline.
Watchdog forwards received telemetry to LGTM by default. Its own telemetry goes
through its [sidecar](../architecture/self_telemetry.md) at
`http://127.0.0.1:4319`, and the sidecar exports to LGTM by default.
It also scrapes the Operator's metrics endpoint with the `nktp-operator` job in
its `prometheus` receiver. See [Topology](../architecture/topology.md#self-telemetry)
for every source of self-telemetry.

The bundled OpenTelemetry Operator manages the watchdog. Autoscaling requires the
Kubernetes resource metrics API, commonly provided by metrics-server.
Setting `watchdog.kubernetes.targetAllocator.enabled` to `false` will cause Helm
rendering to fail.
The allocator has independent resource and security settings under
`watchdog.kubernetes.targetAllocator`. See
[allocator configuration](node_collector.md#target-allocator) for its security defaults.

Configure the Collector through `watchdog.otelcol.config`.

The receiver configuration keys are `otlp/secure` for mTLS gRPC on port 4317
and `otlp/insecure` for plaintext gRPC on port 4320. The secure receiver accepts
metrics, logs, and traces; the insecure receiver accepts allocator metrics.
Default pipelines reference these keys.

Like the signal gateways, watchdog's secure gRPC receiver sends GOAWAY after
five minutes (+/-10% jitter), with 30 seconds for in-flight exports to finish.
This lets clients reach newly added replicas while limiting connection churn.
Tune `keepalive.server_parameters` under
`watchdog.otelcol.config.receivers.otlp/secure.protocols.grpc` for the required
scale-up relief and connection churn. Reconnects can select the same replica.
See [gRPC connection management](https://github.com/grpc/proposal/blob/master/A9-server-side-conn-mgt.md).

The watchdog and its sidecar set `k8s.cluster.name` from `global.clusterName`
with their `transform/cluster` processors, unless the telemetry already has one.
The sidecar needs its own processor because the watchdog's own telemetry goes
from the sidecar straight to the backend. When `global.clusterName` is empty,
the value is `unknown`.

Watchdog uses persistent exporter queues by default: approximately 82MiB of
serialized telemetry per signal, 246MiB total, sharing one `1Gi` volume per replica. Provide an
SSD-backed StorageClass. Its sidecar keeps its own queues in memory. See
[Configure exporter queue storage](../guides/configure_exporter_queue_storage.md)
for outage sizing, volume retention, upgrade behavior, and the in-memory
alternative.

Use `watchdog.kubernetes` for Collector CR settings and `watchdog.otelcol.image`
to override the global `otelcol_image`.
Include `GOMEMLIMIT` and `GOGC` in any replacement `watchdog.kubernetes.env` list
to retain these settings.
The Collector Pod inherits `priorityClass.name`; override
`watchdog.kubernetes.priorityClassName` to use another class or `""` to opt out.
To grant this Collector more Kubernetes API access, see
[Kubernetes API access](node_collector.md#kubernetes-api-access).
The examples there are for the node Collector; bind the rules to this Collector's
service account from the table in that section instead.

## Redaction

Watchdog masks recognized credentials, including quoted JSON fields and Basic/Bearer
credentials, in received telemetry with `****`, following
[OWASP logging guidance](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html#data-to-exclude).
Other collectors' diagnostic exports must pass through watchdog because
[internal error logs can include samples of rejected data](https://opentelemetry.io/docs/collector/internal-telemetry/#events-observable-with-internal-logs).

Matching covers metric data point, resource, and scope attributes. Span names,
OTLP `Metric.metadata`, and exemplar `filtered_attributes` pass through unchanged.

Watchdog's sidecar redacts watchdog's own logs, metrics, and traces before export,
along with its own logs and metrics. Configure its rules under
`watchdog.sidecar.otelcol.config.processors`: `redaction/metrics_v1` for metrics
and `redaction/basic_v1` for logs and traces. The metrics rules match explicit
credential keys and preserve object identifiers such as KSM's `secret` label.
Watchdog keeps
`basic` tracing and the sidecar's own tracing stays disabled.
