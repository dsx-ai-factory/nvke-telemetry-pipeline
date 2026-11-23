# Size resources

Use one sizing file from the same release as the chart:

- [xlarge](../examples/sizing-xlarge.values.yaml) matches the chart defaults.
- [small](../examples/sizing-small.values.yaml) lowers CPU requests and halves the covered Collectors' queue sizes.

Each file covers gateways, watchdog, node and large-target collectors, cluster
events, their Target Allocators and KSM. Sidecars and the nodeless collector and
allocator keep chart defaults.
Neither requires the other. Without a sizing file, the chart uses `xlarge` sizing.

First follow the [installation setup](../../nktp/README.md#install) to register
Helm repositories, build dependencies, and create a namespace with the
`privileged` Pod Security label. From the repository root, set the release and
namespace below and create `my-values.yaml` with your deployment settings:

```console
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
helm upgrade --install "$NKTP_RELEASE" ./nktp --namespace "$NKTP_NAMESPACE" \
  --values docs/examples/sizing-small.values.yaml \
  --values my-values.yaml
```

Later files override earlier ones. Helm merges maps but replaces lists. When overriding `env`, include the full
list, including required variables such as `POD_NAMESPACE` and Go memory settings.

For example, to override the logs gateway CPU request and queue size:

```yaml
gateway_logs:
  kubernetes:
    resources:
      requests:
        cpu: 100m
  otelcol:
    config:
      exporters:
        otlp_grpc:
          sending_queue:
            queue_size: 300000000
```

The following `xlarge` budgets are per Pod.

| Component | CPU / memory request | CPU / memory limit |
| --- | --- | --- |
| Node collector | 500m / 1 GiB | 2 / 2 GiB |
| Large-target collector | 500m / 2 GiB | 4 / 12 GiB |
| Nodeless collector | 100m / 256 MiB | 1 / 1 GiB |
| Metrics gateway | 250m / 512 MiB | 6 / 4 GiB |
| Logs and traces gateways | 250m / 512 MiB | 4 / 4 GiB |
| Cluster event reader | 250m / 256 MiB | 1 / 512 MiB |
| Watchdog | 100m / 256 MiB | 4 / 1 GiB |
| Node Target Allocator | 250m / 2 GiB | 2 / 4 GiB |
| Large-target, nodeless and watchdog Target Allocators | 250m / 1 GiB | 1 / 2 GiB |
| Self-telemetry sidecar, watchdog and gateways | 20m / 64 MiB | 200m / 256 MiB |
| Self-telemetry sidecar, other Collectors | 10m / 32 MiB | 100m / 128 MiB |

Node collectors use a `768MiB` `GOMEMLIMIT`. Gateways use 1.5 GiB,
large-target collectors use 7 GiB, nodeless collectors use 384 MiB and
watchdog uses 192 MiB. These runtime memory targets are configured in
`kubernetes.env`; container limits are in `kubernetes.resources.limits`.
With `xlarge`, node collector queues hold 64 MiB per signal, including in-flight payloads.
Gateway and watchdog queues use persistent storage by default. Their memory
budgets cover active requests, processing, and database caching. See
[Configure exporter queue storage](configure_exporter_queue_storage.md) for volume
capacity, outage tolerance, and the memory-queue alternative.

With `small`:
Gateway CPU requests are 50m with a CPU HPA target of 231%.
Watchdog uses 25m and 200%, and large-target collectors use 125m and 283%.
Node collectors, cluster events, and the node, watchdog and large-target
allocators request 100m. KSM requests 25m per node and 100m per deployment.
Memory, Go settings, sidecar and nodeless resources, replica counts and memory
HPA targets match `xlarge`.

HPA targets are relative to requests. Increasing limits does not raise the
scaling threshold. The component dashboards show observed CPU and resident
memory in **CPU cores · top 20** and **Memory (RSS) · top 20**, for example
[NKTP Metrics Gateway](../../nktp/dashboards/default/nktp-gateway-metrics.json).

See [Large-target metrics](../components/large_target_metrics.md#sizing-large-scrapes)
for sizing the pool for large scrape responses, and
[Backpressure](../architecture/backpressure.md) for how queues and the memory limiter
behave when a component is saturated. See
[Self-telemetry sidecars](../architecture/self_telemetry.md#sizing) for how the
sidecar sizes were chosen.
