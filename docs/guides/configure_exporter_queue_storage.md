# Configure exporter queue storage

This guide covers two ways to store the watchdog's and gateways' exporter
queues:

- **With persistent storage (default):** keep queued telemetry on a PersistentVolume
  across Collector restarts, using the
  [`file_storage` extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.162.0/extension/storage/filestorage).
  The default Collector image includes the extension.
- **Without persistent storage:** keep queued telemetry in
  memory. A Collector restart loses its queued telemetry.

Both use the Collector's
[sending queue](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/exporterhelper/README.md#sending-queue),
configured through the chart's values.

See OpenTelemetry's
[persistent storage overview](https://opentelemetry.io/docs/collector/resiliency/#persistent-storage-write-ahead-log---wal)
for restart recovery and remaining data-loss scenarios. This guide covers
NKTP's volume configuration and the memory-queue alternative.

Both queue types buffer telemetry before export, even while the backend is
available. Both use the exporter's retry settings: a batch that still fails
after `retry_on_failure.max_elapsed_time`, 30 minutes by default, is dropped.

## Size queues for outage tolerance

Choose serialized queue capacity first, for either storage option. Then use
[Size the volumes](#size-the-volumes) or [Size memory](#size-memory) to budget
the storage needed for that capacity.

With `sizer: bytes`, `queue_size` limits the size of serialized telemetry
accepted by each sending queue. **It does not measure or limit the queue's
actual memory usage or the space allocated to its database files on the
volume.**

Size each queue from the peak serialized telemetry rate **per replica** and
the outage duration to buffer:

```text
queue_size (bytes) = serialized bytes/second × outage seconds
```

To measure serialized bytes/s, enable detailed internal metrics on the
Collector being sized. For example:

```yaml
gateway_metrics:
  otelcol:
    config:
      service:
        telemetry:
          metrics:
            level: detailed
```

In your Prometheus-compatible backend, query the
[`otelcol_exporter_enqueue_size_bytes`](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/exporterhelper/documentation.md#otelcol_exporter_enqueue_size_bytes)
histogram's sum to get serialized bytes/s per replica:

```promql
sum by (instance) (
  rate(otelcol_exporter_enqueue_size_bytes_sum{job="nktp-gateway-metrics", exporter="otlp"}[5m])
)
```

Replace the job with the component being sized. Use periods with zero enqueue
failures (**Enqueue failures** in
[NKTP Health](../../nktp/dashboards/default/nktp-health.json)): this histogram
records enqueue attempts, so retries after failures can count telemetry again.
This metric is available at detailed telemetry level and is not currently shown in
the bundled dashboards. Watchdog's histogram combines its three signals;
using that combined rate to size each signal queue gives a conservative budget.

Increase `retry_on_failure.max_elapsed_time` on the exporter when retries must
continue beyond the default 30 minutes.
For broader pipeline resilience, follow OpenTelemetry's
[recommendations for preventing data loss](https://opentelemetry.io/docs/collector/resiliency/#recommendations-for-preventing-data-loss).

## Monitor sending queues

For either storage option, check these panels:

- **Exporter queue use · top 20** in the component dashboards, such as
  [NKTP Metrics Gateway](../../nktp/dashboards/default/nktp-gateway-metrics.json),
  shows `otelcol_exporter_queue_size / otelcol_exporter_queue_capacity`.
  Growing queues indicate export is falling behind. Check both the gateway
  and the Collectors sending to it: growing sender queues can indicate that
  the gateway is slow to accept telemetry.
- **Enqueue failures** in
  [NKTP Health](../../nktp/dashboards/default/nktp-health.json) shows
  `otelcol_exporter_enqueue_failed_metric_points` and its log/span equivalents.
  With `block_on_overflow: true`, full queues make requests wait, so queue
  growth can precede failures.

The gateway HPA scales using CPU and memory utilization. Blocked requests can
increase memory enough to trigger scaling; queue growth can also occur while
CPU and memory remain below the scaling thresholds. Use these panels to
observe queue pressure independently of autoscaling.

For a fresh installation with either queue type, first complete the
[chart README's Helm repository, namespace creation, and Pod Security setup](../../nktp/README.md#install).
Use that namespace in the installation commands below.

## Installing NKTP with persistent storage

### Prerequisites

Provide an **SSD-backed StorageClass** that can provision a volume for each
watchdog and gateway replica. Check the available classes:

```console
kubectl get storageclass
```

The provisioner and
[StorageClass parameters](https://kubernetes.io/docs/concepts/storage/storage-classes/#parameters)
determine its backing storage. NKTP uses the cluster's default StorageClass
when `storageClassName` is omitted. If that
class is unsuitable or there is no default, set `storageClassName` in each
Collector's `kubernetes.volumeClaimTemplates`. Claims awaiting provisioning
leave Collector Pods `Pending`.

### Size the volumes

After choosing `queue_size`, budget storage for all queues in each replica.
We do not have a validated storage overhead factor for production workloads.
**Start with 2× the combined serialized queue capacities** for database
overhead and free space, rounded up to a whole GiB:

```text
starting volume request = round up (2 × sum of serialized queue capacities)
```

Watchdog has separate logs, metrics, and traces queues: `queue_size` applies to
each signal. Although its configuration defines `otlp` once, using that exporter
in three pipelines creates three independent queues sharing the volume.
The defaults apply the starting budget as follows:

| Collector | `sending_queue.queue_size` (serialized bytes per signal) | Serialized capacity per replica | Volume request per replica |
| --- | ---: | --- | --- |
| Watchdog | `515396076` | Approximately 0.48GiB per signal, 1.44GiB total | `3Gi` |
| Each gateway | `515396076` | Approximately 0.48GiB | `1Gi` |

The [volume measurement](../../tests/scale/backend-outage/README.md#measure-persistent-queue-volume-usage-and-io)
observed peak allocated storage of 144.6MiB and peak serialized queue occupancy
of 114.9MiB, about **1.26×**. This limited synthetic metrics experiment on a
Kind cluster informs the 2× starting budget. It does not establish a reliable
factor for production workloads; payloads, database allocation, and filesystems
can produce greater overhead. Storage providers may also require larger
minimum volumes.

The volume request is set in
`kubernetes.volumeClaimTemplates[].spec.resources.requests.storage`, as shown
in the [values example](../examples/persistent_queue.values.yaml).
Increasing the PVC alone leaves `queue_size` unchanged.

Each replica that has run keeps its own claim. The watchdog autoscales between
2 and 16 replicas and each gateway between 2 and 128, so at their maximum that
is up to 400 volumes, subject to the storage provider's minimum volume size.
`kubernetes.autoscaler.maxReplicas` controls each component's replica ceiling.
With `whenScaled: Retain`, telemetry queued on a removed replica is sent when that replica scales back out. With
`Delete`, it is discarded.

### Configure the queue

The default configuration gives each gateway replica a retained `1Gi` claim
and each watchdog replica a retained `3Gi` claim. Each claim is named `queue`
and mounted at `/var/lib/otelcol/queue`. The exporter
uses `file_storage/queue` at `/var/lib/otelcol/queue/data`. Sidecars and
Collectors running on each node keep their queues in memory.

If the default StorageClass is SSD-backed, install with the chart defaults.
To select another class, edit `your-ssd-class` in the
[values example](../examples/persistent_queue.values.yaml) for all four
Collectors. From the repository root, edit the release name and namespace
below, then install or upgrade:

```console
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
helm dependency build nktp
helm upgrade --install "$NKTP_RELEASE" ./nktp --namespace "$NKTP_NAMESPACE" \
  --values docs/examples/persistent_queue.values.yaml
```

The default `file_storage.fsync: false` follows the extension's default.
Queued data can survive a Collector process restart; an abrupt node or power
failure can lose unsynchronized writes. Set `fsync: true` under
`<component>.otelcol.config.extensions.file_storage/queue` to synchronize each
write, with the performance cost described in the
[extension documentation](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.162.0/extension/storage/filestorage/README.md).

Volume mounts and `service.extensions` are lists, which Helm replaces. Preserve
both the queue and internal TLS mounts, and both `health_check` and
`file_storage/queue`, when combining overrides such as
[Secure gateway export](secure_gateway_export.md).

### Storage performance

Persistent queues write telemetry before export, even while the backend is
healthy. Use SSD-backed storage: the Collector's storage extension uses bbolt,
which [benefits significantly from SSDs](https://github.com/etcd-io/bbolt/blob/v1.5.0/README.md#caveats--limitations).
Storage latency and throughput can limit ingestion and backlog recovery.

For AWS EBS, follow AWS's
[guide to resolving I/O, queue length, and latency issues](https://repost.aws/knowledge-center/ebs-volume-io-queue-latency-issues).
The [Verify](#verify) commands identify the PersistentVolume; its
`spec.csi.volumeHandle` identifies the EBS volume.

### Change the claim templates

Kubernetes does not allow changing the volume claim templates of a
StatefulSet. When they are added or changed, the OpenTelemetry Operator deletes
the StatefulSet and creates it again, so every replica of that Collector
restarts at once. Senders retry while it is unavailable. `whenDeleted: Retain`
keeps the claims through this, and the new replicas reuse them as they are.
With `Delete`, the queued data is deleted.

When switching from memory queues, use **Exporter queue use · top 20** in the
component dashboards, such as
[NKTP Metrics Gateway](../../nktp/dashboards/default/nktp-gateway-metrics.json),
to check the backlog over time. If utilization keeps rising, defer the switch
until export catches up and the backlog subsides.

Once utilization is low and recovers between traffic bursts, proceed with the
Helm upgrade above using normal graceful termination. The Collector
[stops receivers before flushing queued exports](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/service/internal/graph/graph.go#L433-L441),
so a small, transient backlog can drain during replacement. NKTP allows 60
seconds through `<component>.kubernetes.terminationGracePeriodSeconds`.
Export failures or expiry of that grace period can still lose buffered data;
sender retries cannot recover telemetry the terminating Collector already accepted.

A larger claim template applies to new claims. For existing claims, follow
Kubernetes' [volume expansion procedure](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#expanding-persistent-volumes-claims),
which requires a StorageClass that allows expansion. The
[Verify](#verify) commands show requested and provisioned capacity.

### Delete the claims

Retained claims stay after NKTP is uninstalled. Edit the namespace below, then
delete them when they are no longer needed:

```console
NKTP_NAMESPACE=nktp
for collector in watchdog gateway-logs gateway-metrics gateway-traces; do
  kubectl delete pvc --namespace "$NKTP_NAMESPACE" \
    --selector "app.kubernetes.io/instance=$NKTP_NAMESPACE.nktp-$collector"
done
```

### Verify

To list requested capacity, provisioned capacity, and the bound volume for
each claim, edit the namespace below:

```console
NKTP_NAMESPACE=nktp
kubectl get pvc --namespace "$NKTP_NAMESPACE" \
  -o custom-columns='CLAIM:.metadata.name,REQUESTED:.spec.resources.requests.storage,CAPACITY:.status.capacity.storage,VOLUME:.spec.volumeName'
```

To inspect the EBS volume ID or local storage path, replace `QUEUE_PV` below
with a name from the `VOLUME` column:

```console
QUEUE_PV=pvc-replace-with-volume-name
kubectl get pv "$QUEUE_PV" -o yaml
```

## Installing NKTP without persistent storage

### Size memory

After choosing `queue_size`, budget memory for all queues in each replica.
Decoded data points, attribute maps, strings, and other in-memory structures
increase the heap needed to retain that telemetry. We do not have a validated heap overhead
factor for production workloads. **Start with 4× the combined serialized queue
capacities**, plus baseline and processing memory and headroom:

```text
starting memory budget = 4 × sum of serialized queue capacities
                         + baseline and processing memory + headroom
```

The [heap measurement](../../tests/scale/backend-outage/README.md#measure-in-memory-queue-heap-overhead)
observed **3.38×** additional retained heap per serialized queue byte after
subtracting baseline heap. This limited synthetic metrics experiment on a Kind
cluster informs the 4× starting budget. It does not establish a reliable factor
for production workloads; attribute counts, value sizes, and signal types can
produce greater overhead. Baseline and processing memory also include active
requests and memory outside the Go heap.

| Collector | Memory example `sending_queue.queue_size` (serialized bytes per signal) | Serialized capacity per replica | Memory limit per replica |
| --- | ---: | --- | --- |
| Watchdog | `8388608` | 8MiB per signal, 24MiB total | `1Gi` |
| Each gateway | `515396076` | Approximately 0.48GiB | `4Gi` |

Choose serialized queue capacity using
[Size queues for outage tolerance](#size-queues-for-outage-tolerance), then
account for heap overhead in the memory budget. The container memory limit is
`<component>.kubernetes.resources.limits.memory`. The **Memory (RSS) · top 20**
panel in [NKTP Metrics Gateway](../../nktp/dashboards/default/nktp-gateway-metrics.json)
and [NKTP Watchdog](../../nktp/dashboards/default/nktp-watchdog.json) shows
observed resident memory. [Size resources](size_resources.md) lists the default
container limits and runtime memory targets.

### Configure the queue

Use the [memory-queue values example](../examples/memory_queue.values.yaml) to
keep watchdog and gateway queues in memory. From the repository root, edit
the release name and namespace, then install or upgrade:

```console
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
helm dependency build nktp
helm upgrade --install "$NKTP_RELEASE" ./nktp --namespace "$NKTP_NAMESPACE" \
  --values docs/examples/memory_queue.values.yaml
```

The example sets `sending_queue.storage` and the `file_storage/queue`
extension to `null` so Helm removes the inherited map entries. It replaces
`service.extensions` and `volumeMounts` with lists retaining `health_check` and
the internal TLS mount, and replaces `volumeClaimTemplates` with an empty list.
The example uses 8MiB per watchdog signal and approximately 0.48GiB per
gateway, with the memory limits listed in [Size memory](#size-memory).

Switching an existing installation to memory restarts the affected Collectors,
as described in [Change the claim templates](#change-the-claim-templates).
Telemetry stored in retained claims is replayed when the Collector uses those
persistent queues. The
[Delete the claims](#delete-the-claims) commands remove the claims and their
stored telemetry.
