# Nodeless metrics

The nodeless metrics pool (`nodeless_metrics`) scrapes targets that have no
Kubernetes node, such as Probes for external URLs, Ingresses or Services, and
ScrapeConfigs with static targets. Monitoring resources need no label to opt in.

The node collector's Target Allocator assigns each target to the Collector on
the target's node. A target without a node would stay unassigned and never be
scraped. This pool's Target Allocator selects the same ServiceMonitor,
PodMonitor, Probe, and ScrapeConfig resources as the node collector's, and uses
`consistent-hashing`, so each of its targets is scraped by one replica, wherever
it runs. Monitors labeled `nktp.nvidia.io/large-target: "true"` go to the
[large-target pool](large_target_metrics.md) instead.

Each allocator has a default scrape class that splits targets by node. A target
has a node when `__meta_kubernetes_pod_node_name`, `__meta_kubernetes_node_name`
or `__meta_kubernetes_endpoint_node_name` is set, or when its EndpointSlice
address targets a `Node` by name. This is how the Target Allocator's
`per-node` strategy places targets. The node collector's class keeps only
targets with a node, and this pool's class drops them, so no target is scraped
by both. The allocators apply these relabelings before allocating targets.

A monitor that sets `scrapeClassName` skips both default classes. Its targets
with a node are then scraped by both pools, and its targets without one by
this pool only. To use your own scrape class, add the matching `keep` or `drop`
relabeling from `nktp/values.yaml` to it.

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

Declare each set of endpoints with a Probe; this pool scrapes it. Edit the
namespace, the targets and the module before applying:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: Probe
metadata:
  name: example-endpoints
  namespace: nktp
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
