# Configure a persistent exporter queue

The watchdog and the gateways queue telemetry in memory by default, so a
Collector that restarts loses what it had queued. NKTP does not configure a
persistent queue. This guide documents OpenTelemetry Collector configuration,
set through the chart's values, that keeps each exporter's
[queue](https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/exporterhelper/README.md#persistent-queue)
on a PersistentVolume with the
[`file_storage` extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/v0.162.0/extension/storage/filestorage).
The default Collector image includes the extension.

The persistent queue stores telemetry before export, even while the backend is
available. Persisting it on a volume does not change retries: a batch that
still fails after `retry_on_failure.max_elapsed_time`, 30 minutes by default,
is dropped. Raise it on the `otlp` exporter to keep data through longer outages.

## Prerequisites

A StorageClass that can provision volumes. Check that the cluster has a default
one:

```console
kubectl get storageclass
```

Without a default StorageClass, the volume claims stay `Pending` and so do the
Pods. Set `storageClassName` in each claim template to use a class that is not
the default.

## Configure the queue

From the repository root, edit the release name and namespace below to match
your installation, then install or upgrade with the
[values example](../examples/persistent_queue.values.yaml), which configures
the watchdog and all three gateways:

```console
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
helm dependency build nktp
helm upgrade --install "$NKTP_RELEASE" ./nktp --namespace "$NKTP_NAMESPACE" \
  --values docs/examples/persistent_queue.values.yaml
```

For each Collector it adds a claim template, mounts the volume, makes it
writable by the Collector's user, and points the exporter's queue at it. For
the watchdog:

```yaml
watchdog:
  kubernetes:
    podSecurityContext:
      fsGroup: 10001
      fsGroupChangePolicy: OnRootMismatch
    volumeClaimTemplates:
      - metadata:
          name: queue
        spec:
          accessModes: [ReadWriteOnce]
          # storageClassName: <class>
          resources:
            requests:
              storage: 2Gi
    persistentVolumeClaimRetentionPolicy:
      whenDeleted: Retain
      whenScaled: Retain
    volumeMounts:
      - name: internal-tls-server
        mountPath: /etc/nktp/internal-tls/server
        readOnly: true
      - name: queue
        mountPath: /var/lib/otelcol/queue
  otelcol:
    config:
      extensions:
        file_storage/queue:
          directory: /var/lib/otelcol/queue
      exporters:
        otlp:
          sending_queue:
            storage: file_storage/queue
            queue_size: 515396076
      service:
        extensions: [health_check, file_storage/queue]
```

Volume mounts and `service.extensions` are lists, which Helm replaces, so the
example restates the chart's internal TLS mount and `health_check`. Merge the
lists by hand when combining it with another example, such as the one in
[Secure gateway export](secure_gateway_export.md). Helm merges
`sending_queue`, so its other default settings stay in place.

## Size the volumes

With `sizer: bytes`, `queue_size` limits the size of serialized telemetry
accepted by each sending queue. **It does not measure or limit the queue's
actual memory usage or the space allocated to its database files on the
volume.** The example uses:

| Collector | `sending_queue.queue_size` (serialized bytes per signal) | Serialized capacity per replica | Volume request per replica |
| --- | ---: | --- | --- |
| Watchdog | `515396076` | Approximately 0.48GiB per signal, 1.44GiB total | `2Gi` |
| Each gateway | `515396076` | Approximately 0.48GiB | `1Gi` |

Size each replica's volume for all its queues plus database overhead, then
round up to a whole GiB. Watchdog has separate logs, metrics, and traces
queues: `queue_size` applies to each signal, not their combined size.
Although its configuration defines `otlp` only once, using that exporter in
the logs, metrics, and traces pipelines creates three independent queues,
each with the full `queue_size` capacity.
In the [persistent gateway queue test](../../tests/scale/backend-outage/README.md#measure-persistent-queue-volume-usage-and-io),
a 128MiB queue reached 114.9MiB of serialized telemetry and peak allocated
storage of 138.3MiB, about 20% above peak queue occupancy. Using 20% overhead as
an estimate, watchdog needs `3 × 515396076 × 1.2` bytes, about 1.728GiB,
rounded up to `2Gi`. Each gateway has one signal queue and requests `1Gi`.
Overhead varies by workload; monitor volume usage to avoid failed queue writes
when it fills.

Increase `queue_size` to buffer more telemetry through longer backend outages,
just as with an in-memory queue. Increase the volume request to fit the larger
queues and storage overhead, and review the retry duration as well.

Request at least the provider's minimum volume size. OCI block volumes have a
minimum of 50GB.

Each replica that has run keeps its own claim. The watchdog autoscales between
2 and 16 replicas and each gateway between 2 and 128, so at their maximum that
is up to 400 volumes, or 20TB at the OCI minimum. Lower
`autoscaler.maxReplicas` to bound it. With `whenScaled: Retain`, telemetry
queued on a removed replica is sent when that replica scales back out. With
`Delete`, it is discarded.

## Storage performance

Persistent queues write telemetry to the storage database before export, even
when the backend is healthy. Storage latency, IOPS, and sustained read/write
throughput can therefore limit normal ingestion and backlog recovery. The
volume sizes above provide capacity, not a performance guarantee; NKTP has no
validated minimum IOPS or throughput requirement.

Validate the intended StorageClass under representative load, including
draining a backlog while new telemetry arrives.

## Change the claim templates

Kubernetes does not allow changing the volume claim templates of a
StatefulSet. When they are added or changed, the OpenTelemetry Operator deletes
the StatefulSet and creates it again, so every replica of that Collector
restarts at once. Senders retry while it is unavailable. Make this change during
a quiet period. `whenDeleted: Retain` keeps the claims through this, and the new
replicas reuse them as they are. With `Delete`, the queued data is deleted.

## Delete the claims

Retained claims stay after NKTP is uninstalled. Edit the namespace below, then
delete them when they are no longer needed:

```console
NKTP_NAMESPACE=nktp
for collector in watchdog gateway-logs gateway-metrics gateway-traces; do
  kubectl delete pvc --namespace "$NKTP_NAMESPACE" \
    --selector "app.kubernetes.io/instance=$NKTP_NAMESPACE.nktp-$collector"
done
```

## Verify

The `otelcol_exporter_queue_size` metric of each Collector shows queued data.
Each replica has a bound claim named after its Pod. Edit the namespace below,
then list them:

```console
NKTP_NAMESPACE=nktp
kubectl get pvc --namespace "$NKTP_NAMESPACE" | grep -E "nktp-(watchdog|gateway)-"
```
