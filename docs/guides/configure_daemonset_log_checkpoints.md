# Configure node log checkpoints

The node Collector persists log read positions on each node by default, using
the `file_storage` extension. This requires write access to a node hostPath
directory, `/var/lib/nktp/log-checkpoints` by default.

- **With persistent checkpoints (default):** resume from saved read positions
  after a Collector restart or Pod replacement on the same node.
- **Without persistent checkpoints:** keep read positions in memory and apply
  each receiver's `start_at` setting after a restart.

Checkpoints cannot recover logs deleted by rotation or state lost when a node
is replaced.

## With persistent checkpoints (default)

No overrides are needed. The `daemonset` section in
[chart defaults](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/nktp/values.yaml) defines the storage configuration.

Each node must allow the Collector to write to `/var/lib/nktp/log-checkpoints`.
SELinux policies, non-root users, or read-only node filesystems may require
adjusted permissions or a different host directory. If host writes are
unavailable, use the [configuration without persistent checkpoints](#without-persistent-checkpoints).

The host directory is mounted at `/var/lib/otelcol/log-checkpoints` inside the
Collector. To change the host location, edit `hostPath.path` on the
`log-checkpoints` entry in `daemonset.kubernetes.volumes`; the container mount
and extension directory can remain unchanged.

The `file_log` and `journald` receivers reference `file_storage/log-checkpoints`
through their `storage` settings. The extension is
registered alongside `health_check` in
`daemonset.otelcol.config.service.extensions`. Include a `storage` reference
when adding a log reader.

Helm replaces `volumes` and `volumeMounts` lists in full. Preserve host-log,
checkpoint, and TLS mounts when customizing these lists or combining examples.

For rolling updates, keep
`daemonset.kubernetes.daemonSetUpdateStrategy.rollingUpdate.maxSurge` at `0`
(the Kubernetes default). Surge updates can start two Collectors on the same
node, where they contend for the same checkpoint database files.

## Without persistent checkpoints

Use the [memory-checkpoint values example](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/docs/examples/memory_log_checkpoints.values.yaml)
to disable persistent checkpoints. It removes receiver storage references,
the storage extension, and the checkpoint volume and mount, while retaining
host-log and TLS mounts.

Merge the example into the values file you already use for NKTP, including
your custom volume and mount entries in its replacement lists.

After a restart, readers using `start_at: end` skip records written while they
were unavailable; readers using `beginning` can reread retained files.

## Verify

With persistent checkpoints enabled, inspect the checkpoint files on a node
running the Collector. Use your cluster's kubeconfig and edit the namespace
and node below. Node debugging requires permission to create privileged debug
Pods; see Kubernetes' [node debugging instructions](https://kubernetes.io/docs/tasks/debug/debug-cluster/kubectl-node-debug/).

```sh
NKTP_NAMESPACE=nktp
NKTP_NODE=your-node-name
kubectl get pods --namespace "$NKTP_NAMESPACE" \
  --selector app.kubernetes.io/name=nktp-daemonset-collector -o wide
kubectl debug "node/$NKTP_NODE" --namespace "$NKTP_NAMESPACE" \
  --profile=sysadmin --attach=true \
  --image=public.ecr.aws/docker/library/busybox:1.37.0 -- \
  ls -la /host/var/lib/nktp/log-checkpoints
```

Expect checkpoint files for the log receivers. Adjust the path if you selected
another host directory. The debug command prints its Pod name; edit the name
below and remove that Pod after inspection:

```sh
DEBUG_POD=node-debugger-replace-with-pod-name
kubectl delete pod --namespace "$NKTP_NAMESPACE" "$DEBUG_POD"
```

## Cleanup

Disabling checkpoints or uninstalling NKTP leaves checkpoint files on each
node. Once the Collector has stopped and the saved positions are no longer
needed, delete `/var/lib/nktp/log-checkpoints` from each node, or your configured
host path. Deleting these files resets saved read positions.
