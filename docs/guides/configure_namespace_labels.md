# Configure namespace labels

NKTP does not create or own its release namespace. The namespace must have
`pod-security.kubernetes.io/enforce: privileged` before NKTP workloads start,
because the node Collector and node-exporter use host paths and namespaces.

If your platform already manages namespace metadata, configure this label there
and leave `namespace.labeling.enabled: false` (the default).

## Enable labeling with Helm

For Helm to configure the label, add this to your overrides file:

```yaml
namespace:
  labeling:
    enabled: true
```

Run checkout commands from the repository root after reviewing the chart's
[prerequisites](../../nktp/README.md#prerequisites) and
[building dependencies](../../nktp/README.md#install). Use your intended
cluster's kubeconfig, edit the release, namespace and overrides-file path below,
then install:

```bash
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
NKTP_VALUES_FILE=/path/to/my-values.yaml
helm install "$NKTP_RELEASE" ./nktp --namespace "$NKTP_NAMESPACE" \
  --create-namespace --values "$NKTP_VALUES_FILE" --wait
```

Helm creates the namespace before storing its release, then runs the labeling
Job before NKTP workloads. The same idempotent Job runs before upgrades and can
start under `restricted` Pod Security enforcement. It only gets permission to
read and patch the release namespace; it does not change namespace ownership.
`task install` honors this setting in its values file and already passes
`--create-namespace`. When upgrading with `--reset-values`, keep the option in
your overrides file if you want the hook to run again.

The installer must be allowed to create the helper ServiceAccount, ClusterRole
and ClusterRoleBinding and grant `get`/`patch` access to the release namespace.
Override `namespace.labeling.image` to choose a kubectl image, or
`global.imageRegistry` to use a mirror. Set `namespace.labeling.resources` to
adjust the Job's requests and limits.

## Deployment integrations

For Argo CD, use `CreateNamespace=true` or platform automation to create the
destination namespace first. Argo CD
[maps the Helm hooks to PreSync](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/#helm-hooks).
For Kustomize, kapp and other render-and-apply tools, arrange for the hook's
ServiceAccount and RBAC to exist before the Job, and wait for the Job before
applying workloads. If the integration cannot enforce this ordering, manage
the namespace label through platform automation and leave the hook disabled.

## Uninstall and cleanup

Uninstalling NKTP or disabling the hook leaves the namespace and its label
unchanged. Successful hook Jobs are deleted automatically. The helper
ServiceAccount and RBAC remain between runs; the next enabled install or
upgrade recreates them. After uninstalling, you can remove these helpers
separately using the release and namespace settings above:

```bash
kubectl delete job,serviceaccount,clusterrole,clusterrolebinding \
  --namespace "$NKTP_NAMESPACE" --ignore-not-found \
  --selector "app.kubernetes.io/name=nktp-namespace-labeling,app.kubernetes.io/instance=$NKTP_RELEASE,nktp.nvidia.io/release-namespace=$NKTP_NAMESPACE"
```
