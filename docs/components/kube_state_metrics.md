# kube-state-metrics

The DaemonSet under `kube-state-metrics.daemonset` collects scheduled Pods on its node.
Configure central workloads under `kube-state-metrics.deployments`. Each central workload's `resource_types`
selects the Kubernetes resource types to collect and grants matching RBAC.
Set `track_unscheduled_pods: true` to collect Pods without a node assignment.
This is supported only for central workloads and requires `resource_types` to be exactly `[pods]`.
Resource names come from the [upstream resource list](https://github.com/kubernetes/kube-state-metrics/blob/v2.20.0/docs/README.md#exposed-metrics).

`mutatingadmissionpolicies` and `mutatingadmissionpolicybindings` are disabled by
default because KSM uses their `admissionregistration.k8s.io/v1` APIs, which older
clusters do not serve. Add them to `resource_types` only when the cluster serves both APIs.

`kube-state-metrics.namespaces` sets the namespace scope unless a workload supplies
its own `namespaces` list. RBAC follows the effective namespace selection.
An empty list means all namespaces. Empty strings are ignored.

Set `kube-state-metrics.metricLabelsAllowlist` to expose selected Kubernetes labels,
for example `["pods=[app.kubernetes.io/name]"]`. Additional Kubernetes labels are
disabled by default.

Set `kube-state-metrics.metricAnnotationsAllowlist` to expose selected Kubernetes
annotations in the `kube_<resource>_annotations` metrics, for example
`["secrets=[example.com/cert-serial]", "nodes=[example.com/state-*]"]`. The format
matches KSM's `--metric-annotations-allowlist`. Both allowlists apply to every
workload. Annotations are disabled by default.

Secret metrics are opt-in because collection requires list/watch access to complete
Secret objects, including their contents. To enable them, set
`kube-state-metrics.deployments.configuration.resource_types` to `[configmaps, secrets]`.
Set that workload's `namespaces` list to limit access. An empty list grants access
across the cluster.

## Custom resources

A central workload can also report custom resources with KSM's
[custom-resource-state metrics](https://github.com/kubernetes/kube-state-metrics/blob/v2.20.0/docs/metrics/extend/customresourcestate-metrics.md).
Set `customResourceState` on the workload:

- `config` is the custom-resource-state configuration. The chart stores it in the
  ConfigMap `nktp-ksm-<workload>-custom-resource-state`, mounts it read-only, and
  passes it with `--custom-resource-state-config-file`. KSM reloads the file when
  the ConfigMap changes.
- `rules` lists the RBAC rules KSM needs to read the custom resources. Each rule
  needs `apiGroups` and `resources`; `verbs` defaults to `[list, watch]`. The rules
  are granted cluster-wide, whatever the workload's `namespaces` list. The chart
  also grants list and watch on `customresourcedefinitions`, which KSM uses to
  discover the resources.

With `customResourceState`, `resource_types` may be an empty list. The workload
then runs with `--custom-resource-state-only` and collects no core resources. The
DaemonSet does not support `customResourceState`.

This example adds a workload that reports each Argo CD `Application` as an info
metric, `kube_customresource_argocd_application_info`, with the source revision
as a label:

```yaml
kube-state-metrics:
  deployments:
    argocd:
      enabled: true
      resource_types: []
      shards: 1
      customResourceState:
        config:
          spec:
            resources:
              - groupVersionKind:
                  group: argoproj.io
                  version: v1alpha1
                  kind: Application
                metrics:
                  - name: argocd_application_info
                    help: Argo CD Application source revision.
                    each:
                      type: Info
                      info:
                        labelsFromPath:
                          name: [metadata, name]
                          target_revision: [spec, source, targetRevision]
        rules:
          - apiGroups: [argoproj.io]
            resources: [applications]
```

## Kubernetes settings

Shared `kubernetes` defaults can be overridden under each workload. `resources`,
`env` and `securityContext` configure the KSM container, `podSecurityContext`
configures Pod security, and remaining fields configure the Pod spec.
Set `serviceAccount` to use an existing account.

KSM Pods inherit `priorityClass.name` when non-empty, including when
`priorityClass.create: false` references an existing class. Override
`kube-state-metrics.kubernetes.priorityClassName` for all KSM Pods, or set
`kubernetes.priorityClassName` under `daemonset` or `deployments.<name>` for a
specific workload. An explicit `""` opts out of inheritance.

When overriding scheduling settings, ensure the node Collector runs on every node
where the KSM DaemonSet runs so it can scrape the local KSM endpoint.

Increase `shards` when a central workload exceeds its memory or scrape response
budget. Helm supplies `--shard` and `--total-shards`. Each shard runs as one
Deployment and briefly stops collection during replacement.

The [kube-state-metrics design](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/docs/designs/kube_state_metrics.md) compares the
approaches considered for scaling KSM collection.
