# Nodeless metrics

The nodeless metrics pool (`nodeless_metrics`) selects ServiceMonitor,
PodMonitor, Probe, and ScrapeConfig resources with the metadata label
`nktp.nvidia.io/nodeless-target: "true"`.
Apply this label to monitoring resources whose targets have no Kubernetes node,
such as Probes for external URLs, Ingresses or Services, and ScrapeConfigs with
static targets.

The node collector's Target Allocator assigns each target to the Collector on
the target's node. A target without a node stays unassigned and is never
scraped. This pool's Target Allocator uses `consistent-hashing` instead, so every
target it selects is scraped by one replica, wherever it runs.

The node collector's Target Allocator excludes monitors with this label, so a
target is not scraped twice. A monitor labeled both
`nktp.nvidia.io/nodeless-target: "true"` and `nktp.nvidia.io/large-target: "true"`
is scraped by the [large-target pool](large_target_metrics.md) only.

Collectors refresh their assigned targets every 10 seconds. Configure this with
`nodeless_metrics.kubernetes.targetAllocator.collectorTargetReloadInterval`.

The Target Allocator defaults `prometheusCR.denyFSAccessThroughSMs` to `false`
for compatibility with existing monitors that reference local bearer-token or
TLS files. See
[Restrict monitor file access](../guides/restrict_monitor_file_access.md).

Setting `nodeless_metrics.kubernetes.targetAllocator.enabled` to `false`
will cause Helm rendering to fail. Target Allocator telemetry is sent to watchdog.
The allocator has its own security contexts and resource budget under
`nodeless_metrics.kubernetes.targetAllocator`, and read access to Ingresses for
Probes that discover them.

Collected metrics carry the `nktp_metrics_pool: nodeless` label and go to
`nktp-gateway-metrics-collector:4317` over OTLP/gRPC.
Each Collector sends its own telemetry through its
[sidecar](../architecture/self_telemetry.md) to watchdog.
Use `nodeless_metrics.otelcol.config` for Collector internal configuration and
`nodeless_metrics.kubernetes` for workload configuration.
Collector Pods inherit `priorityClass.name`; override
`nodeless_metrics.kubernetes.priorityClassName` to use another class or `""`
to opt out.

## Sizing

The pool runs 2 replicas without an autoscaler. Each Collector requests 100m CPU
and 256 MiB of memory, with limits of 1 CPU and 1 GiB. The memory limiter starts
refusing above 563 MiB of Go heap. Targets in this pool are expected to be small:
probe results and static endpoints. Label endpoints with large responses
`nktp.nvidia.io/large-target: "true"` instead. To scrape more targets, raise
`nodeless_metrics.kubernetes.replicas`; the Target Allocator spreads targets
across replicas.

## Blackbox probing

NKTP bundles
[prometheus-blackbox-exporter](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-blackbox-exporter),
disabled by default. Enable it to probe HTTP, TCP, DNS, gRPC, or ICMP endpoints:

```yaml
blackbox-exporter:
  enabled: true
```

It runs 2 replicas behind the `nktp-blackbox-exporter` Service on port 9115.
Its own metrics are declared with a ServiceMonitor, which the node collector
scrapes because the exporter's Pods have a node. Configure probe modules under
`blackbox-exporter.config.modules` using the upstream chart's values.

Declare each set of endpoints with a Probe labeled for this pool. Edit the
namespace, the targets and the module before applying:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: Probe
metadata:
  name: example-endpoints
  namespace: nktp
  labels:
    nktp.nvidia.io/nodeless-target: "true"
spec:
  prober:
    url: nktp-blackbox-exporter.nktp.svc:9115
  module: http_2xx
  interval: 30s
  targets:
    staticConfig:
      static:
        - https://example.com/healthz
```

Use the release namespace in `prober.url` if NKTP is installed elsewhere. The
same Probe works with a blackbox exporter that NKTP does not manage; point
`prober.url` at it.
