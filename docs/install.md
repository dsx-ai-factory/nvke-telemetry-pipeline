# Install and upgrade NKTP

This page installs and upgrades NKTP from the published Helm chart. For
readiness checks, connecting a backend, and setting the cluster name, continue
with [Getting started](getting-started.md). To install from a repository
checkout, follow the chart README's
[installation instructions](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/nktp/README.md#install).

## Prerequisites

Provide the following before installing NKTP.

- **Kubernetes 1.31 or later.** Every Collector Pod runs its self-telemetry
  sidecar as a native sidecar, an init container with `restartPolicy: Always`,
  which Kubernetes enables by default from 1.29.
- **Helm 3.** Tested with Helm 3.20. Choose a Helm release
  [compatible with your Kubernetes version](https://helm.sh/docs/v3/topics/version_skew/).
- **cert-manager.** Required with default values; install it separately. NKTP
  does not bundle or install it. It issues certificates for internal Collector
  mTLS, the Operator's admission webhook, and Collector-to-Target-Allocator
  mTLS. Running without it requires replacing all three integrations. See
  [Configure cert-manager](guides/configure_cert_manager.md) for both options.
- **SSD-backed persistent storage.** Required with default values for gateway
  and watchdog exporter queues. Each replica requests a `1Gi` claim; minimum
  replica counts require eight volumes totaling 8Gi. Claims use the cluster's
  default StorageClass unless `storageClassName` is set in each component's
  `kubernetes.volumeClaimTemplates`. Without a suitable class, claims may remain
  pending. If suitable storage is unavailable, configure in-memory queues. See
  [Configure exporter queue storage](guides/configure_exporter_queue_storage.md)
  for both options.
- **Image registry access** to `docker.io`, `ghcr.io`, `quay.io` and
  `registry.k8s.io`, or mirrors of them. Set `global.imageRegistry` to override
  the registry of all components. The `metrics-server` dependency, the nested
  OpenTelemetry Operator, and the uninstall hook's kubectl image
  (`opentelemetry-kube-stack.cleanupJob.image`) do not read it, so set their
  image repositories explicitly when you use a mirror.
- **Network access** between components. The Kubernetes API server must reach
  the Operator's admission webhook on TCP 9443 on the nodes; managed clusters
  can block this with firewall rules or security groups. Node collectors scrape
  their node's kubelet on TCP 10250 and node-exporter on TCP 9100. Collectors
  send OTLP to the gateways and watchdog across nodes. Control plane component
  metrics depend on the distribution, which determines whether those components
  expose reachable metrics endpoints.
- **Kubernetes resource metrics API** (`metrics.k8s.io`). Required for gateway,
  large-target and watchdog autoscaling. NKTP does not install metrics-server by
  default; set `metrics-server.enabled=true` if nothing else in the cluster
  provides the API.
- **Privileged Pod Security level for the release namespace.** The node
  collector mounts host paths for pod logs, journald and `/proc`. node-exporter
  uses the host network and PID namespaces, host paths, and host port 9100. The
  `baseline` and `restricted` Pod Security levels reject these Pods.
- **Writable node hostPath storage for log checkpoints.** Required with default
  values to persist node log read positions. If host writes are unavailable,
  disable persistent checkpoints. See
  [Configure node log checkpoints](guides/configure_daemonset_log_checkpoints.md)
  for both options.
- **Capacity.** The defaults are sized for large clusters: about 5.2 CPU and
  14.9 GiB of memory in requests for the cluster-wide components, plus about
  0.6 CPU and 1.2 GiB on every node for the per-node DaemonSets. Allow extra
  headroom for cluster services, prerequisites, the bundled LGTM backend, and
  autoscaling. See [Size resources](guides/size_resources.md).

## Install

You need `kubectl` and Helm configured to use the cluster. Edit the
assignments below before running the commands: set `NKTP_VERSION` to the chart
version to install, and `CLUSTER_NAME` to your cluster's name. If the namespace
already exists, skip creating it, and make sure it has the privileged Pod
Security label.

```bash
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
NKTP_VERSION=0.2.0
CLUSTER_NAME=my-cluster
```

Add the chart repository:

```bash
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
helm repo update nvidia
```

Create the namespace with the privileged Pod Security label, then install:

```bash
kubectl create namespace "$NKTP_NAMESPACE"
kubectl label namespace "$NKTP_NAMESPACE" pod-security.kubernetes.io/enforce=privileged

helm install "$NKTP_RELEASE" nvidia/nktp \
  --version "$NKTP_VERSION" \
  --namespace "$NKTP_NAMESPACE" \
  --set global.clusterName="$CLUSTER_NAME" \
  --wait
```

If the cluster has no metrics API provider, add
`--set metrics-server.enabled=true` to the `helm install` command. To
customize the installation, put your settings in a values file and add
`--values /path/to/my-values.yaml` instead of `--set` flags.

The Operator creates the Collectors after `helm install --wait` returns, so
check their Pods as well. See
[Check readiness](getting-started.md#check-readiness).

By default, everything exports to a bundled LGTM backend that is for evaluation
only. Before production, see [Set a backend](getting-started.md#set-a-backend).

## Upgrade

1. Review the [upgrade notes](upgrading.md) for your target version, and
   merge the required changes into your Helm values file.
2. If the target chart version updates the bundled OpenTelemetry or Prometheus
   CRDs, apply the updated CRDs before `helm upgrade`. Helm does not upgrade
   CRDs installed from a chart's `crds/` directory, and leaving outdated CRDs in
   place can cause fields introduced by the new version to be dropped.
3. Upgrade with `--reset-values`. It starts from the new chart version's
   defaults and applies the settings in your values file, without carrying
   forward previously stored settings. Settings you passed with `--set`, such as
   `global.clusterName`, are not carried forward either, so keep every setting
   you want to retain in your values file.

The Operator recreates watchdog and gateway StatefulSets when their volume
claim templates change, restarting their Collectors. Queue claims are retained
by default. See
[Change the claim templates](guides/configure_exporter_queue_storage.md#change-the-claim-templates).

Edit the assignments, then upgrade. Set `NKTP_VERSION` to the target version,
and `NKTP_VALUES_FILE` to your values file.

```bash
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
NKTP_VERSION=0.2.0
NKTP_VALUES_FILE=/path/to/my-values.yaml

helm repo update nvidia
helm upgrade "$NKTP_RELEASE" nvidia/nktp \
  --version "$NKTP_VERSION" \
  --namespace "$NKTP_NAMESPACE" \
  --reset-values \
  --values "$NKTP_VALUES_FILE" \
  --wait
```

If you installed with chart defaults and no values file, omit the `--values`
line and pass your `--set` flags again.

After the upgrade returns, check the Collectors again as described in
[Check readiness](getting-started.md#check-readiness).
