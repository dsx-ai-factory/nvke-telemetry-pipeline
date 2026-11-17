# Gateways

NKTP runs one gateway per signal. Each gateway accepts OTLP over gRPC with mTLS
at its Service in the Helm release namespace, forwards what it receives to LGTM
by default, and sends its own metrics, logs and traces through its
[sidecar](../architecture/self_telemetry.md) to watchdog.

| Gateway | Values key | Default gRPC endpoint |
| --- | --- | --- |
| Logs | `gateway_logs` | `dns:///nktp-gateway-logs-collector-headless:4317` |
| Metrics | `gateway_metrics` | `dns:///nktp-gateway-metrics-collector-headless:4317` |
| Traces | `gateway_traces` | `dns:///nktp-gateway-traces-collector-headless:4317` |

Gateways accept OTLP/gRPC on port 4317 by default. An HTTP receiver requires
explicit configuration, including TLS, under
`<gateway>.otelcol.config.receivers.otlp.protocols.http`.

## Collector connections

Node Collectors send logs, metrics and traces to their gateways over OTLP/gRPC
with mutual TLS. The large-target and nodeless pools send metrics, and the
cluster-events reader sends logs. These exporters use the headless endpoints
above with `balancer_name: round_robin`. The `dns:///` prefix selects gRPC DNS
resolution, exposing gateway Pod IPs so exports are distributed across ready
connections to replicas. TLS uses `server_name_override` to verify
`nktp-gateway-<signal>-collector`, so existing certificates remain valid during
upgrades. Gateway certificates also include the headless Service names.

All three gateways also send GOAWAY at a connection age of one minute, with
+/-10% jitter, to rotate connections. In-flight exports get another 30 seconds
to finish before forced closure. See
[gRPC connection management](https://github.com/grpc/proposal/blob/master/A9-server-side-conn-mgt.md).

Tune `max_connection_age` under
`<gateway>.otelcol.config.receivers.otlp.protocols.grpc.keepalive.server_parameters`
based on gateway headroom and acceptable delay after scale-up. Lower it for
faster relief. Raise it to reduce TCP setup and TLS handshakes.
DNS refresh and connection turnover determine when clients discover new replicas.
Increase `max_connection_age_grace` if in-flight exports need longer to finish.
Explicitly configured HTTP receivers do not use these gRPC age settings.

Exporter overrides use `daemonset.otelcol.config.exporters.otlp_grpc/logs`,
`otlp_grpc/metrics` and `otlp_grpc/traces`, and
`large_target_metrics.otelcol.config.exporters.otlp_grpc`,
`nodeless_metrics.otelcol.config.exporters.otlp_grpc` and
`cluster_events.otelcol.config.exporters.otlp_grpc`.
When changing a destination, set `tls.server_name_override` to its certificate
name or `""` to verify the endpoint name.

Use `<gateway>.otelcol.config` for Collector configuration and
`<gateway>.kubernetes` for Kubernetes workload configuration. Autoscaling
requires the Kubernetes resource metrics API. Retain `GOMEMLIMIT` and `GOGC` when
replacing `<gateway>.kubernetes.env`.
To grant the gateways more Kubernetes API access, see
[Kubernetes API access](node_collector.md#kubernetes-api-access).
The examples there are for the node Collector; bind the rules to each gateway's
service account from the table in that section instead.

Each gateway stores approximately 0.48 GiB of serialized telemetry in a
persistent sending queue on a per-replica `1Gi` volume. Provide an SSD-backed
StorageClass. Active requests, processing, and database caching consume memory.
Set `exporters.otlp_grpc.sending_queue.queue_size` for the required outage
tolerance and increase the claim capacity to fit. See
[Configure exporter queue storage](../guides/configure_exporter_queue_storage.md)
for sizing, volume retention, upgrade behavior, and the in-memory alternative.

Gateways inherit `priorityClass.name` (default `nktp-telemetry-critical` at
`1000000000`, below system-critical Pods), including their sidecars; set
`priorityClass.preemptionPolicy: Never` to disable preemption. Use an existing
class with `priorityClass.create: false`, or override
`<gateway>.kubernetes.priorityClassName` per gateway (`""` opts out).

Each gateway sets `k8s.cluster.name` from `global.clusterName` with its
`transform/cluster` processor, unless the telemetry already has one. When
`global.clusterName` is empty, the value is `unknown`. The chart passes the name
in the `K8S_CLUSTER_NAME` environment variable, and the processor reads it
inside an OTTL string, so names such as `42` or `true` stay strings. The name
must not contain a double quote or a backslash.

To export to a production backend, see
[Set a backend](../getting-started.md#set-a-backend) and
[Secure gateway export](../guides/secure_gateway_export.md).
