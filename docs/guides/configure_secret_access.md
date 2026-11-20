# Configure Secret access

Target Allocators resolve Secret and CA ConfigMap references in ServiceMonitors,
PodMonitors, Probes, and ScrapeConfigs. These objects live in the monitor's
namespace. Monitor discovery and credential access are separate permissions:
discovering a monitor does not authorize reading its credentials.

## Default: no chart-managed Secret access

`grantClusterSecretAccess` defaults to `false`. NKTP creates no Secret grants,
including in its release namespace. Unauthenticated scraping can continue, but
monitors that reference credentials cannot resolve them unless the allocator
already has the required permissions. The upstream Secret metadata watcher can
log authorization errors without Secret list/watch permission, even when
monitors do not reference Secrets.

Turning this option off removes the chart's cluster grant. It does not revoke
permissions granted by other Roles or ClusterRoles. With the option off and no
explicit native `prometheusCR.secretNamespaces` list, the upstream watcher
watches the release namespace.

## Upgrade from chart-managed namespace grants

Earlier charts created a Role and RoleBinding for the node allocator in each
namespace listed in
`daemonset.kubernetes.targetAllocator.prometheusCR.secretNamespaces` when
`daemonset.rbac.create` was `true`. This version removes that namespace-RBAC
management and the `daemonset.rbac.create` option. Helm deletes the old grants
on upgrade; retaining `secretNamespaces` alone does not preserve permission.
Credentialed monitors can then lose access to their Secrets or CA ConfigMaps.

To use the replacement chart-managed grant, explicitly set
`grantClusterSecretAccess: true` in the values file used for the upgrade, after
reviewing its cluster-wide scope below. With the default `false`, the chart
creates no replacement for the removed namespace grants. Retaining
`secretNamespaces` alone does not preserve credentialed scraping.

After upgrading, verify that credentialed scrape targets remain healthy and
check the allocator logs for authorization errors.

## Opt in: cluster-wide credential access

From the repository root, add this to your Helm values file and apply it through
your normal install or upgrade process:

```yaml
grantClusterSecretAccess: true
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
