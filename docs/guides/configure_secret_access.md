# Configure Secret access

When a ServiceMonitor, PodMonitor, Probe, or ScrapeConfig references a Secret or
CA ConfigMap, the Target Allocator running in the NKTP release namespace needs
RBAC authorization to read that object in the monitor's namespace. Configure
node namespace access when the namespaces are known and the node allocator is
responsible for the monitors, or enable cluster-wide access for all
monitor-watching allocators. Node namespace access alone does not authorize the
nodeless allocator that also reads ordinary monitors.

## Default and precedence

`targetAllocatorClusterSecretAccess` defaults to `false`, so cluster-wide reads remain
opt-in. The node allocator also retains its existing namespace-RBAC option:

| Configuration | Chart-managed RBAC (Secrets `get/list/watch`, ConfigMaps `get`) |
| --- | --- |
| `targetAllocatorClusterSecretAccess: true` | Cluster-wide access for the monitor-watching node, large-target, and nodeless allocators. Redundant node namespace grants are skipped. |
| `targetAllocatorClusterSecretAccess: false`, `daemonset.rbac.create: true`, and a nonempty node `secretNamespaces` list | Node allocator access in each listed namespace. |
| `targetAllocatorClusterSecretAccess: false` and `daemonset.rbac.create: false` | No cluster-wide or node namespace RBAC for Secrets or ConfigMaps. |

With the default empty node namespace list, the chart creates no RBAC allowing
Secret or ConfigMap reads, including in the release namespace.
`daemonset.rbac.create` controls only node namespace RBAC; the cluster-wide
option is independent of that setting.

## Configure node namespace access

To grant the node allocator access in specific namespaces, keep the
cluster-wide switch off and list those namespaces:

```yaml
targetAllocatorClusterSecretAccess: false
daemonset:
  rbac:
    create: true
  kubernetes:
    targetAllocator:
      prometheusCR:
        secretNamespaces: [metrics-monitors]
```

For each listed namespace, Helm creates a
`nktp-daemonset-monitor-<release-namespace>` Role and RoleBinding,
granting Secrets `get/list/watch` and ConfigMaps `get` to the node allocator's
ServiceAccount. The binding honors
`daemonset.kubernetes.targetAllocator.serviceAccount`. `daemonset.rbac.create`
defaults to `true`; an omitted, empty, or null list creates no namespace grants.
The namespaces must already exist.

These permissions cover all Secrets in each listed namespace, including objects
not referenced by monitors. Monitor selectors do not narrow the RBAC grant.
Namespace grants currently apply only to the node allocator. They do not grant
access to the large-target or nodeless allocators. With default selectors, node
and nodeless allocators read the same monitors before splitting their targets,
so a node-only grant does not supply all permissions needed for shared monitors.

## Opt in: cluster-wide credential access

From the repository root, add this to your Helm values file and apply it through
your normal install or upgrade process:

```yaml
targetAllocatorClusterSecretAccess: true
```

The chart creates a ClusterRole and ClusterRoleBinding granting:

- Secrets: `get`, `list`, and `watch` across all namespaces.
- ConfigMaps: `get` across all namespaces, for CA references.

The grant applies to the node, large-target, and nodeless Target Allocators with
monitor discovery enabled. It excludes watchdog, which does not discover
monitors. Existing `<component>.kubernetes.targetAllocator.serviceAccount`
overrides are honored, including externally provided accounts. Collector
ServiceAccounts receive no additional permissions.

This authorizes reading every Secret's contents, including Secrets not referenced
by monitors. The allocator's use of a metadata informer does not restrict the
RBAC grant to metadata. Monitor labels and namespace selectors do not narrow it.
Enable the switch only when this access matches the cluster's operating model.

The chart defaults these allocators' Secret watches to all namespaces using the
upstream `secretNamespaces: [""]` convention. An explicit nonempty native
`<component>.kubernetes.targetAllocator.prometheusCR.secretNamespaces` list is
preserved and can narrow the watch, but the RBAC grant remains cluster-wide.

## Troubleshoot Secret watch authorization errors

The Target Allocator lists and watches Secret metadata to detect credential
changes, even when its selected monitors do not reference Secrets. With no
explicit `prometheusCR.secretNamespaces` list, it watches the NKTP release
namespace. Without Secret `list/watch` authorization there, its logs can contain
an error like this example:

```text
secrets is forbidden: User "system:serviceaccount:nktp:nktp-daemonset-targetallocator" cannot list resource "secrets" in API group "" in the namespace "nktp"
```

Check the affected allocator's `secretNamespaces` and RBAC in each watched
namespace. A metadata watch still requires Secret `list/watch` permission;
monitor discovery does not supply it.

For changes to an existing installation, see
[Upgrade NKTP](../upgrading.md#cluster-wide-secret-access-is-opt-in).
