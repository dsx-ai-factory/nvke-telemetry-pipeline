# Node collector

The node Collector (`daemonset`) runs on every node. It collects the node's logs
and metrics and forwards OTLP that workloads send to it.

## OTLP endpoint

The node Collector accepts OTLP logs, metrics, and traces at
`nktp-daemonset-collector:4317` over gRPC and `http://nktp-daemonset-collector:4318`
over HTTP. The Service routes traffic only to the Collector on the sender's node.
If no local Collector is ready, traffic fails rather than reaching another node.
The Collector sets `k8s.node.name` and forwards each signal to its corresponding
gateway. Qualify the Service name with the NKTP namespace when
sending from another namespace.

## Pod logs and metrics

The DaemonSet reads pod logs from `/var/log/pods` and sends them to the logs
gateway. A dedicated Target Allocator assigns monitored metric endpoints only
to collectors on the same node. Metrics go to
`nktp-gateway-metrics-collector:4317` over OTLP/gRPC, which requires the metrics gateway.
Per-process host metrics are not collected by default. Node exporter provides
host metrics, and the kubelet cAdvisor scrape provides per-container resource
use. See [Enable host process metrics](../guides/enable_host_process_metrics.md)
to enable the `host_metrics` process scraper.

Each DaemonSet collector also scrapes the kubelet `/metrics` and
`/metrics/cadvisor` endpoints on its own node. See
[Configure kubelet metrics](../guides/configure_kubelet_metrics.md) to change these scrapes, verify
kubelet certificates, or run NKTP alongside kube-prometheus-stack.

## Journal

The DaemonSet also collects systemd journal entries at info priority and above
and forwards them to the logs gateway. Nodes must provide `/usr/bin/journalctl`
and `/etc/machine-id`, with journals in `/var/log/journal`. The Collector uses
selective read-only host mounts and
the `SYS_CHROOT` capability to run the host executable. The `/lib64` host volume
is also mounted at `/hostfs/usr/lib64` for Red Hat library paths. Collection
starts from the saved cursor after a restart, or new entries when no cursor
exists. Read positions are stored in `/var/lib/nktp/log-checkpoints` on the node.
See [Configure node log checkpoints](../guides/configure_daemonset_log_checkpoints.md)
for storage requirements and the memory-only alternative.

Override `root_path`, `journalctl_path`, and `directory` under
`daemonset.otelcol.config.receivers.journald` for a different host layout.
Update `daemonset.kubernetes.volumes` and `volumeMounts` to match, retaining
the other mounts when replacing either list. For runtime journals, add a
`/run/log/journal` host volume mounted at `/hostfs/run/log/journal` and set the
receiver directory to `/run/log/journal`.

See [Log classification](#log-classification) for the default audit and
control plane log sources.

## Log classification

NKTP classifies logs by source using the `nvidia.telemetry.class` resource
attribute. All records from a classified source receive the same class,
regardless of their content. The class is forwarded to the logs gateway for
use in routing rules.

| Source | Classification | `service.name` | Recognition |
| --- | --- | --- | --- |
| sudo and SSH journal entries | `system.auth` | `journald` | Journal producer identifier is `sudo` or `sshd`. |
| Linux authentication files | `system.auth` | `auth` | All records from `/var/log/auth.log` or `/var/log/secure`. |
| Linux auditd | `system.audit` | `auditd` | All records from `/var/log/audit/audit.log`, including authentication records. |
| Kubernetes control plane logs | `k8s.control_plane` | Not set by NKTP | Pods in `kube-system` with matching pod-name prefixes and container names for `kube-apiserver`, `kube-controller-manager`, `kube-scheduler`, or `etcd`. |
| Kubernetes API audit logs | `k8s.api.audit` | `kube-apiserver` | Records from `/var/log/kubernetes/audit/audit.log`. API audit logging must be enabled on the API server. |

Other locally collected logs remain unclassified. All journal records use
`journald` as `service.name`. Shared authentication files use `auth`. Service
names reach the logs gateway alongside the classification.

## Self-telemetry

The collector's own metrics, logs and traces go through its
[sidecar](../architecture/self_telemetry.md) to watchdog.
Target Allocator telemetry is sent to watchdog.
Each collector also reads Target Allocator stdout and stderr logs on its node
in the release namespace and sends them to watchdog through `logs/targetallocator`.
Operator manager container logs go to watchdog through `logs/operator`.

## Configuration

Use `daemonset.otelcol.config` for Collector configuration and
`daemonset.kubernetes` for Kubernetes workload configuration. The pod-log mount
is read-only, but host file permissions require the collector to run as root.
File offsets and journal cursors persist across restarts through a dedicated
writable host mount. See
[Configure node log checkpoints](../guides/configure_daemonset_log_checkpoints.md)
to customize the host directory or keep read positions in memory.
Retain `GOMEMLIMIT` and `GOGC` when replacing its environment list.
The chart adds `NODE_NAME`, `POD_NAMESPACE`, and `OTEL_K8S_NODE_IP` if the list
omits them.

The node Collector inherits `priorityClass.name` (default
`nktp-telemetry-critical` at `1000000000`, below system-critical Pods), including
its sidecar but not its Target Allocator. Use an existing class with
`priorityClass.create: false`, or override
`daemonset.kubernetes.priorityClassName` (`""` opts out); set
`priorityClass.preemptionPolicy: Never` to disable preemption.

## Kubernetes API access

The node Collector's service account can only `get` `nodes/metrics`, which the
kubelet scrapes need. Processors that read the Kubernetes API, such as
`k8s_attributes`, need more. The chart has no setting for extra Collector
permissions. Apply your own ClusterRole and ClusterRoleBinding alongside the
release, bound either to the service account the Operator creates or to one you
create yourself.

### Example: workload names on pod logs

Pod log records already carry `k8s.namespace.name`, `k8s.pod.name`,
`k8s.pod.uid` and `k8s.container.name`, taken from the log file path. This
overlay adds the owning workload's name with the `k8s_attributes` processor.
Each Collector watches only the pods on its own node, named by the `NODE_NAME`
environment variable, which the chart sets on the node Collector. Records are
matched to pods by `k8s.pod.uid`. Records without it, such as journal entries,
pass through unchanged.

Helm replaces lists instead of merging them, so the overlay repeats the whole
logs pipeline `processors` list from `values.yaml`. Keep `redaction/basic_v1`
last.

```yaml
daemonset:
  otelcol:
    config:
      processors:
        k8s_attributes:
          auth_type: serviceAccount
          passthrough: false
          filter:
            node_from_env_var: NODE_NAME # Watch only pods on this Collector's node.
          pod_association:
            - sources:
                - from: resource_attribute
                  name: k8s.pod.uid
          extract:
            metadata:
              - k8s.deployment.name
              - k8s.statefulset.name
              - k8s.daemonset.name
              - k8s.job.name
      service:
        pipelines:
          logs:
            processors: [memory_limiter, resource/node, k8s_attributes, redaction/basic_v1]
```

This configuration needs `get`, `list` and `watch` on pods and namespaces. The
processor derives `k8s.deployment.name` from the ReplicaSet name, so it needs no
ReplicaSet access. Extracting `k8s.deployment.uid`, or labels and annotations
from Deployments or ReplicaSets, does need `get`, `list` and `watch` on `apps`
ReplicaSets. Other extracted fields can need other resources; see the
[`k8s_attributes` processor README](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.164.0/processor/k8sattributesprocessor/README.md).
Apply the RBAC before enabling the processor.

### Option 1: bind to the Operator's service account

The Operator creates a service account named `<Collector name>-collector` for
each Collector. For the node Collector, that is `nktp-daemonset-collector`. Save
this manifest as `k8s-attributes-rbac.yaml`, and set the subject's `namespace`
to the release namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: nktp-daemonset-k8s-attributes
rules:
  - apiGroups: [""]
    resources: [pods, namespaces]
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: nktp-daemonset-k8s-attributes
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nktp-daemonset-k8s-attributes
subjects:
  - kind: ServiceAccount
    name: nktp-daemonset-collector
    namespace: nktp # The release namespace.
```

Apply it from the directory that contains it:

```bash
kubectl apply -f k8s-attributes-rbac.yaml
```

This option depends on the Operator's service account naming.

### Option 2: use your own service account

Set `daemonset.kubernetes.serviceAccount`, then create that service account and
its RBAC yourself. The Operator does not create a service account that you name.
Create it before installing or upgrading with this setting; Collector Pods
cannot start until it exists.

```yaml
daemonset:
  kubernetes:
    serviceAccount: nktp-node-collector
```

Save this manifest as `k8s-attributes-rbac.yaml`, and set both `namespace`
fields to the release namespace. It uses the same ClusterRole as option 1:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: nktp-node-collector
  namespace: nktp # The release namespace.
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: nktp-daemonset-k8s-attributes
rules:
  - apiGroups: [""]
    resources: [pods, namespaces]
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: nktp-daemonset-k8s-attributes
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nktp-daemonset-k8s-attributes
subjects:
  - kind: ServiceAccount
    name: nktp-node-collector
    namespace: nktp # The release namespace.
```

Apply it from the directory that contains it:

```bash
kubectl apply -f k8s-attributes-rbac.yaml
```

The chart's own Collector bindings follow `<component>.kubernetes.serviceAccount`,
so they need no changes. Target Allocators have separate service accounts, set
with `<component>.kubernetes.targetAllocator.serviceAccount`.

| Collector | Default service account | Chart bindings that follow `kubernetes.serviceAccount` |
| --- | --- | --- |
| `daemonset` | `nktp-daemonset-collector` | `get` on `nodes/metrics` |
| `large_target_metrics` | `nktp-large-target-metrics-collector` | `get` on the `/metrics` non-resource URL |
| `cluster_events` | `nktp-cluster-events-collector` | Events `get/list/watch`, and `get/update` on the election Lease |
| `watchdog` | `nktp-watchdog-collector` | `get` on the Operator's `/metrics`, rendered only when the bundled Operator is enabled with `clusterRole.create` and `manager.metrics.secure` (the defaults) |
| `gateway_logs`, `gateway_metrics`, `gateway_traces` | `nktp-gateway-<signal>-collector` | None |

### Removal

`helm uninstall` does not remove RBAC or service accounts created outside the
chart. Delete them from the directory that contains the manifest:

```bash
kubectl delete -f k8s-attributes-rbac.yaml
```

## Target Allocator

Configure each allocator through `<component>.kubernetes.targetAllocator`.
Its resource budget and security contexts are independent of the Collector's.
Allocators run as non-root with a read-only root filesystem, no privilege
escalation, dropped capabilities, and `RuntimeDefault` seccomp. Size resource
requests and limits for discovered objects and targets.

Setting `daemonset.kubernetes.targetAllocator.enabled` to `false` will cause Helm
rendering to fail.

Monitoring CRs labeled `nktp.nvidia.io/large-target: "true"` are excluded.
Its scrape classes keep only targets that have a node; the
[nodeless metrics pool](nodeless_metrics.md) scrapes the rest.

The Target Allocator defaults `prometheusCR.denyFSAccessThroughSMs` to `false`
for compatibility with existing ServiceMonitors and PodMonitors that reference
local bearer-token or TLS files. Referenced files must exist in each scraping
Collector container. Prefer Secret or CA ConfigMap references when authoring
monitors. Before enabling `denyFSAccessThroughSMs`, ensure that none of the
allocator's selected monitors reference files in the Collector container. See
[Restrict monitor file access](../guides/restrict_monitor_file_access.md).

### Monitor credentials

To grant the allocator access to Secrets and CA ConfigMaps referenced by monitors, list
the namespaces containing those monitors and their referenced objects:

```yaml
daemonset:
  kubernetes:
    targetAllocator:
      prometheusCR:
        secretNamespaces: [metrics-monitors]
```

`daemonset.rbac.create` defaults to `true`. For each explicitly
listed namespace, Helm creates a Role granting Secrets `get/list/watch` and
ConfigMaps `get`, bound to the allocator ServiceAccount in the release namespace.
The binding honors `daemonset.kubernetes.targetAllocator.serviceAccount`.
Omitting the list or setting it to `[]` creates no namespace RBAC. Set
`daemonset.rbac.create: false` to manage these grants externally.

Grants cover all Secrets in each listed namespace. Use namespaces with trusted
monitor authors. Secret and CA references resolve in the monitor's namespace.
The namespaces must already exist. `secretNamespaces` controls Secret watching.
Configure monitor and namespace selectors separately.
