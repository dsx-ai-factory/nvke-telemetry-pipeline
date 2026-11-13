# Upgrade NKTP

Use the [upgrade procedure](install.md#upgrade) for CRD requirements and Helm
commands, after incorporating the changes below.

When an upgrade changes bundled CRDs, review the relevant versions in the
[OpenTelemetry Operator changelog](https://github.com/open-telemetry/opentelemetry-operator/blob/main/CHANGELOG.md)
or [Prometheus Operator changelog](https://github.com/prometheus-operator/prometheus-operator/blob/main/CHANGELOG.md)
and complete any required migrations before applying those CRDs.

## From 0.2.x to 0.3.x

### OpenTelemetry CRDs

The `opentelemetry-kube-stack` update from `0.25.1` to `0.27.0` changes the
`opentelemetrycollectors.opentelemetry.io` and
`instrumentations.opentelemetry.io` CRD schemas. See the upstream
[OpenTelemetry Operator v0.162.0 release notes](https://github.com/open-telemetry/opentelemetry-operator/releases/tag/v0.162.0).

**ACTION REQUIRED:** When upgrading to a chart containing this dependency
update, follow the [README's CRD update procedure](../nktp/README.md#update-crds)
before upgrading NKTP; `helm upgrade` does not update these CRDs.

### Persistent node log checkpoints

The node Collector now saves file offsets and journal cursors to disk so it
can resume reading after a restart. Persistent checkpoints are enabled by
default.

On the first upgrade from memory-only tracking, receivers using `start_at: end`
may skip unread records already present when they start; checkpoints protect
subsequent restarts but cannot recover those skipped records.

**ACTION REQUIRED:** Before upgrading, confirm that the Collector can write to
`/var/lib/nktp/log-checkpoints`, or configure another writable host directory.
To disable persistence, use the
[memory-only values file](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/docs/examples/memory_log_checkpoints.values.yaml). When
merging it into your Helm values file, preserve custom volume and mount entries
and other `daemonset.otelcol.config.service.extensions` entries in its
replacement lists.

**For persistent checkpoints, if your values file overrides any fields listed
below, reconcile those settings before upgrading.** In particular, replacement
volume lists must include the checkpoint volume and mount.

If you use chart defaults and have write access to the default directory,
no values file is needed.

See [Configure node log checkpoints](guides/configure_daemonset_log_checkpoints.md)
for storage options and verification, and the upstream
[file log receiver README](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.164.0/receiver/filelogreceiver/README.md#offset-tracking)
for how file offsets are tracked and stored.

To use a different host directory, set `hostPath.path` on the `log-checkpoints`
entry in `daemonset.kubernetes.volumes`. Copy the complete volumes list from
the [chart defaults](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/nktp/values.yaml) into your values file if you do not
already override it; Helm replaces this list in full. Keep any custom volumes.
The container mount and storage extension directory can remain unchanged.

For persistent checkpoints, check these fields in your values file.

| Field in your values file | Action before upgrading |
| --- | --- |
| `daemonset.kubernetes.volumes` or `daemonset.kubernetes.volumeMounts` | Include the corresponding `log-checkpoints` entry from the [`daemonset` chart defaults](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/nktp/values.yaml) in each list you override. Keep your custom host and TLS mounts. |
| `daemonset.otelcol.config.service.extensions` | Include `file_storage/log-checkpoints` alongside your other extensions. |
| `daemonset.otelcol.config.extensions["file_storage/log-checkpoints"]` | Keep the extension enabled and its `directory` aligned with the checkpoint volume mount. |
| `daemonset.otelcol.config.receivers` | For added log readers, such as `file_log/custom`, set `storage: file_storage/log-checkpoints`. For existing readers, such as `file_log` and `journald`, preserve that storage reference if you override their `storage` field. |
| `daemonset.kubernetes.daemonSetUpdateStrategy.rollingUpdate.maxSurge` | Keep it at `0` so two Collectors on the same node do not open the same checkpoint databases concurrently. |
