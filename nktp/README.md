# NKTP Helm Chart

NVKE Telemetry Pipeline (NKTP) runs OpenTelemetry Collectors on a Kubernetes cluster.

The chart deploys a pipeline that collects a cluster's metrics, logs and traces
and exports them over OTLP: a Collector on every node, a scrape pool for large
metrics endpoints, a scrape pool for targets without a node, a Kubernetes Event
reader, one gateway per signal, and a
watchdog for the pipeline's own telemetry. The bundled OpenTelemetry Operator
manages the Collectors.

This README covers installing, configuring, and developing the chart. The NKTP
documentation, linked at the [end of this README](#more-information), covers
each component in depth.

## Prerequisites

Provide the following before installing NKTP.

- **Kubernetes 1.31+.** The bundled metrics-server 0.8.1 supports 1.31+. Every
  Collector Pod runs its self-telemetry sidecar as a native sidecar, an init
  container with `restartPolicy: Always`, which Kubernetes enables by default
  from 1.29. The bundled OpenTelemetry Operator 0.161.0 itself supports 1.25+.
- **Helm 3.** Tested with Helm 3.20.
- **cert-manager.** Required with default values; install it separately.
  NKTP does not bundle or install it. It issues certificates for internal
  Collector mTLS, the Operator's admission webhook, and
  Collector-to-Target-Allocator mTLS. Running without it requires replacing all
  three integrations. See [More information](#more-information) for the
  "Configure cert-manager" guide covering both options.
- **SSD-backed persistent storage.** Required with default values for gateway
  and watchdog exporter queues. Each replica requests a `1Gi` claim; minimum
  replica counts require eight volumes totaling 8Gi. Claims use the cluster's
  default StorageClass unless `storageClassName` is set in each component's
  `kubernetes.volumeClaimTemplates`. Without a suitable class, claims may
  remain pending. If suitable storage is unavailable, configure
  in-memory queues. See [More information](#more-information) for the
  "Configure exporter queue storage" guide covering both options.
- **Image registry access** to `docker.io`, `ghcr.io`, `quay.io` and
  `registry.k8s.io`, or mirrors of them. See [Image registry](#image-registry).
- **Network access** between components. The Kubernetes API server must reach the
  Operator's admission webhook on TCP 9443 on the nodes; managed clusters can
  block this with firewall rules or security groups. Node collectors scrape their
  node's kubelet on TCP 10250 and node-exporter on TCP 9100. Collectors send OTLP
  to the gateways and watchdog across nodes. Control plane component metrics
  depend on the distribution, which determines whether those components expose
  reachable metrics endpoints.
- **Kubernetes resource metrics API** (`metrics.k8s.io`). Required for gateway,
  large-target and watchdog autoscaling. NKTP does not install metrics-server by
  default; set `metrics-server.enabled: true` if nothing else in the cluster
  provides the API.
- **Privileged Pod Security level for the release namespace.** The node collector
  mounts host paths for pod logs, journald and `/proc`. node-exporter uses the
  host network and PID namespaces, host paths, and host port 9100. The
  `baseline` and `restricted` Pod Security levels reject these Pods.
- **Writable node hostPath storage for log checkpoints.** Required with default
  values to persist node log read positions. If host writes are unavailable,
  disable persistent checkpoints. See [More information](#more-information)
  for the "Configure node log checkpoints" guide covering both options.
- **Capacity.** The defaults are sized for large clusters: about 5.2 CPU and
  14.9 GiB of memory in requests for the cluster-wide components, plus about
  0.6 CPU and 1.2 GiB on every node for the per-node DaemonSets. These budgets
  assume one NKTP installation per cluster. Multiple releases in the same
  cluster are untested and unsupported; shared checkpoint hostPaths are one
  source of contention.

## Install

From a checkout, build the chart's dependencies first. Their archives aren't
committed. Edit the release name and namespace below, and set `NKTP_VALUES_FILE`
to your Helm values file, or keep `nktp/values.yaml` for chart defaults.
If the namespace already exists, omit its creation command; ensure it has the
privileged Pod Security label:

```bash
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
NKTP_VALUES_FILE=nktp/values.yaml
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
helm dependency build nktp
kubectl create namespace "$NKTP_NAMESPACE"
kubectl label namespace "$NKTP_NAMESPACE" pod-security.kubernetes.io/enforce=privileged --overwrite
helm install "$NKTP_RELEASE" ./nktp --namespace "$NKTP_NAMESPACE" \
  --values "$NKTP_VALUES_FILE" --set namespace.create=false --wait
```

The Operator creates the Collectors after `helm install --wait` returns, so
check their Pods as well.

### Namespace rendering for integrations

For render-and-apply workflows, set `namespace.create: true` to render a
`Namespace` named after the release namespace (`--namespace`), with
`pod-security.kubernetes.io/enforce: privileged`:

```yaml
namespace:
  create: true
```

This includes the Namespace in `helm template` output for Kustomize
`helmCharts`, Argo CD and kapp. Let the integration apply that manifest before
namespaced resources, and remove any separate Namespace manifest for the same
namespace. Keep the option disabled if the integration already manages it.

For `helm install` and `helm upgrade --install`, keep `namespace.create: false`
(the default) and create and label the namespace as shown above. Helm's default
release storage needs the namespace before chart resources are created.
`--create-namespace` creates it without the Pod Security label and, with
`namespace.create: true`, conflicts with the chart's Namespace resource.
The install command explicitly sets `namespace.create=false` so values files
shared with render-and-apply integrations can be reused. `task install` also
forces this setting; create and label its target namespace before running it.
Upgrades reject `namespace.create=false` if the live namespace carries this
release's ownership annotations, including when using `task install`.
The check requires permission to read the release Namespace.

The rendered Namespace carries `helm.sh/resource-policy: keep`. Helm retains it
when it leaves the release manifest or the release is uninstalled; other release
resources are deleted normally. A retained namespace needs external lifecycle
management and can require ownership cleanup before reinstalling. Older release
manifests without this annotation need the upgrade procedure below first.

**Deleting the Namespace still deletes everything in it, including resources
outside NKTP.** Helm's keep policy does not protect it from Argo CD pruning or
other tools' deletion policies. Use a dedicated namespace.

Before turning `namespace.create` off in a pruning integration:

1. Keep it `true` while applying the integration's per-resource retention or
   orphan policy to the Namespace. For Argo CD, use your manifest overlay to add
   `argocd.argoproj.io/sync-options: Prune=false,Delete=false`, preserving any
   existing sync options. Sync this change alone and verify the live Namespace
   carries the protection.
2. In a separate change, set `namespace.create: false` and sync with the
   protection still active. Confirm the Namespace and its privileged Pod
   Security label remain. Argo CD can report a skipped prune as out of sync
   during this handoff.
3. Transfer the Namespace to its new manager and orphan it from the old
   integration's tracking/inventory before removing any pruning protection.
   If the integration cannot retain or orphan individual resources, keep
   namespace rendering enabled until you have a supported handoff procedure.

The Helm upgrade guard does not run during Argo CD's `helm template` rendering
and cannot protect this transition.

### Upgrading

Review the upgrade notes for your target version, available through
[More information](#more-information), and merge the required changes into
your Helm values file before upgrading.

If the target chart version updates the bundled OpenTelemetry or Prometheus
CRDs, follow [Update CRDs](#update-crds) before `helm upgrade`. Helm does not upgrade
CRDs installed from a chart's `crds/` directory. Leaving outdated CRDs in place
can cause fields introduced by the new version to be dropped.

The Operator recreates watchdog and gateway StatefulSets when their volume
claim templates change, restarting their Collectors. Queue claims are retained
by default.

Use `--reset-values` for both cases below. It starts from the new chart
version's defaults and applies the settings in your values file, if supplied,
without carrying forward previously stored settings.

These commands use the published NGC chart and can run from any directory.
Edit the release name, existing namespace, target chart version, and kubeconfig
path. `0.2.0` is an example; choose the published version you intend to install:

```bash
NKTP_RELEASE=nktp
NKTP_NAMESPACE=nktp
NKTP_VERSION=0.2.0
export KUBECONFIG=/path/to/cluster.kubeconfig
helm repo add nvidia https://helm.ngc.nvidia.com/nvidia
helm repo update nvidia
```

#### Update CRDs

**Run this procedure only if the upgrade notes identify CRD changes.**

If both CRD groups are managed outside NKTP, skip this section. Their owners
must ensure compatibility with the target OpenTelemetry Operator and the
Prometheus Operator using the monitoring CRDs. If only one group is externally
managed, update only the NKTP-managed group here. If your values file contains
`opentelemetry-kube-stack.crds.install`, remove it before using the per-group
flags: `true` enables both groups and `false` disables both, overriding the
per-group settings. Keep
`opentelemetry-kube-stack.crds.installOtel` or
`opentelemetry-kube-stack.crds.installPrometheus` set to `false` for externally
managed groups in your values file; rendering excludes them. For example, if
your `kube-prometheus-stack` installation already manages the Prometheus CRDs,
set `opentelemetry-kube-stack.crds.installPrometheus: false` and follow that
installation's CRD upgrade procedure.

Before continuing, review compatibility and complete migrations described in
the upgrade notes, available through [More information](#more-information).
Use credentials with permission to update cluster-scoped CRDs.

Render the target published chart using the version and cluster settings above.
First create a temporary directory and save its default values:

```bash
RENDER_DIR=$(mktemp -d)
helm show values nvidia/nktp --version "$NKTP_VERSION" \
  > "$RENDER_DIR/default-values.yaml"
```

If you maintain a Helm values file, change `NKTP_VALUES_FILE` below to its path;
otherwise keep the saved defaults. Use the same values file for the upgrade:

```bash
NKTP_VALUES_FILE="$RENDER_DIR/default-values.yaml"
helm template "$NKTP_RELEASE" nvidia/nktp --version "$NKTP_VERSION" \
  --namespace "$NKTP_NAMESPACE" --include-crds -f "$NKTP_VALUES_FILE" \
  --output-dir "$RENDER_DIR"
find "$RENDER_DIR" -type d -name crds -print
```

Review the diffs for both rendered CRD groups before applying them. Run each
command only if its directory appeared in the listing above; omit commands for
groups that were not rendered:

```bash
OTEL_CRD_DIR="$RENDER_DIR/nktp/charts/opentelemetry-kube-stack/charts/otel-crds/crds"
PROMETHEUS_CRD_DIR="$RENDER_DIR/nktp/charts/opentelemetry-kube-stack/charts/prometheus-crds/crds"
kubectl diff --server-side --field-manager=helm -f "$OTEL_CRD_DIR"
kubectl diff --server-side --field-manager=helm -f "$PROMETHEUS_CRD_DIR"
```

Check that the diffs are consistent with the upstream changelog changes linked
from the upgrade notes. A diff exit status of `1` means changes were found;
greater than `1` means an error to resolve before continuing.

After reviewing the diffs, apply each rendered group you intend to update;
omit commands for groups that were not rendered:

```bash
kubectl apply --server-side --field-manager=helm -f "$OTEL_CRD_DIR"
kubectl apply --server-side --field-manager=helm -f "$PROMETHEUS_CRD_DIR"
```

#### Upgrade the chart

**Check namespace ownership before either upgrade command.** Inspect the stored
release manifest with:

```bash
helm get manifest "$NKTP_RELEASE" --namespace "$NKTP_NAMESPACE"
```

If it includes a `kind: Namespace` resource for the release namespace, keep
`namespace.create=true` until ownership has been safely migrated. Older installed
revisions may lack `helm.sh/resource-policy: keep`: first upgrade to this chart
with `namespace.create=true`, then verify that both the stored release manifest
and the live Namespace carry the keep annotation. A Helm upgrade that omits an
unprotected Namespace deletes it and everything in it; `--create-namespace` does
not prevent this. Do not remove Helm ownership annotations before verifying
retention and arranging external namespace management. `--reset-values`
also resets `namespace.create` to `false`, so add `--set namespace.create=true`
to the defaults command below, or replace the `false` override in the values-file
command with `true`. Use direct `helm upgrade` for these releases: `task install`
forces the option off, and the chart rejects that upgrade when the namespace
carries the release's ownership annotations. Merely changing those annotations
does not remove the Namespace from Helm's stored manifest or safely transfer it.

**If you use chart defaults**, upgrade with the new chart defaults:

```bash
helm upgrade "$NKTP_RELEASE" nvidia/nktp --version "$NKTP_VERSION" \
  --namespace "$NKTP_NAMESPACE" \
  --reset-values --wait
```

**If you maintain a values file**, update it for the new version and supply
it with `-f`. Include all custom settings you want to keep. Merge custom volume
and mount entries into any example lists before applying them.

Edit the values file path below, then upgrade. If the release owns its Namespace,
replace `--set namespace.create=false` with `--set namespace.create=true`:

```bash
NKTP_VALUES_FILE=/path/to/my-values.yaml
helm upgrade "$NKTP_RELEASE" nvidia/nktp --version "$NKTP_VERSION" \
  --namespace "$NKTP_NAMESPACE" \
  --reset-values -f "$NKTP_VALUES_FILE" --set namespace.create=false --wait
```

## Components

| Component | Values key | Runs as | Purpose |
| --- | --- | --- | --- |
| Node collector | `daemonset` | DaemonSet | Receives OTLP from workloads on its node, and collects pod logs, the journal, host and audit logs, kubelet and cAdvisor metrics, and scrape targets on its node. |
| Logs, metrics, and traces gateways | `gateway_logs`, `gateway_metrics`, `gateway_traces` | One StatefulSet each | Export each signal to the backend. |
| Large-target metrics pool | `large_target_metrics` | StatefulSet | Scrapes monitors labeled `nktp.nvidia.io/large-target: "true"`, including the Kubernetes control plane. |
| Nodeless metrics pool | `nodeless_metrics` | StatefulSet | Scrapes targets that have no node, such as Probes and static ScrapeConfigs. |
| Cluster event reader | `cluster_events` | StatefulSet | Reads Kubernetes Events, with one elected replica active at a time. |
| Watchdog | `watchdog` | StatefulSet | Receives the pipeline's own telemetry and exports it to the backend. |
| kube-state-metrics | `kube-state-metrics` | DaemonSet and Deployments | Exposes Kubernetes object state as metrics. |
| OpenTelemetry Operator and CRDs | `opentelemetry-kube-stack` | Deployment | Creates the Collectors and their Target Allocators. |
| node-exporter | `node-exporter` | DaemonSet | Exposes host metrics from every node, tolerating all taints. |
| metrics-server | `metrics-server` | Deployment, off by default | Serves the resource metrics API that the autoscalers read. |
| blackbox-exporter | `blackbox-exporter` | Deployment, off by default | Probes HTTP, TCP, DNS, gRPC and ICMP endpoints for Probe resources. |
| LGTM | `otel-lgtm` | Deployment | Development backend with Grafana, Prometheus, Loki and Tempo. Evaluation only. |

The node collectors, the large-target and nodeless pools, and the event reader send each
signal to its gateway over OTLP/gRPC with mutual TLS, and the gateways export to
the backend.
Each Collector Pod runs a sidecar Collector that buffers the Collector's own
telemetry and samples its traces. All sidecars except watchdog's send to watchdog
over OTLP/gRPC with mTLS on port 4317. The DaemonSet also sends collected Operator
and Target Allocator logs directly to that endpoint. Target Allocators push their
own metrics to watchdog over plaintext OTLP/gRPC on port 4320. Watchdog exports
received telemetry to the backend without passing through the gateways; its own
sidecar exports watchdog's telemetry to the backend.

Gateways rotate connections about every minute and watchdog every five minutes
so clients can reach newly added replicas. Configure the interval under each
receiver's `otelcol.config`.

## Sending telemetry to NKTP

- **OTLP:** send to the node collector on the sender's node, at
  `nktp-daemonset-collector.<namespace>:4317` over gRPC or
  `http://nktp-daemonset-collector.<namespace>:4318` over HTTP. If no local
  Collector is ready, traffic fails rather than reaching another node.
- **Prometheus metrics:** create a `ServiceMonitor` or `PodMonitor`. It can live
  in any namespace and needs no NKTP-specific label. If a NetworkPolicy guards
  the metrics port, admit the release namespace. If a monitor references a
  Secret or CA ConfigMap, list its namespace in
  `daemonset.kubernetes.targetAllocator.prometheusCR.secretNamespaces` so the
  node Target Allocator can read it. Both the node and large-target allocators
  default `prometheusCR.denyFSAccessThroughSMs` to `false` for compatibility
  with monitors that reference files in Collector containers. Before setting
  it to `true`, replace file references in the selected monitors with Secret
  or CA ConfigMap references; otherwise, affected scrape jobs are dropped.
- **Large scrape responses:** label the monitor
  `nktp.nvidia.io/large-target: "true"` to scrape it from the large-target pool,
  which reserves more memory per scrape, instead of the node collector.
- **Probes and targets without a node:** the node collector only scrapes
  targets on its own node, so the nodeless pool scrapes Probes and static
  ScrapeConfigs, with no label needed. Set `blackbox-exporter.enabled: true` to install a
  blackbox exporter for Probes to use, at `nktp-blackbox-exporter.<namespace>:9115`.
- **Logs:** Pod stdout and stderr are collected from every node without
  configuration.

## Configuring

Each Collector component has the same values structure:

| Key | Contents |
| --- | --- |
| `<component>.otelcol.config` | The complete Collector configuration. |
| `<component>.otelcol.image` | An image override for this component only. |
| `<component>.kubernetes` | `OpenTelemetryCollector` settings such as replicas, resources, env, volumes, autoscaler, and Target Allocator. |

The chart renders each Collector configuration exactly as written, unless it
contains NVCB blueprints. If it contains blueprints, these are expanded into
native OTel configuration. To change a pipeline, change its configuration.
See [Use blueprints](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/blob/main/docs/guides/use_blueprints.md)
for more details.

Each gateway defaults to an approximately 0.48 GiB persistent sending queue
measured in serialized bytes. Watchdog has three persistent queues of
approximately 82MiB each, 246MiB total. Each gateway and watchdog replica
requests a `1Gi` volume, retained on scale-down and deletion.
Outage buffering capacity is serialized bytes/s per replica multiplied by the
outage duration. Volume allocation also includes database overhead and is
subject to provider minimums. Monitor queue utilization and enqueue failures.
Set `retry_on_failure.max_elapsed_time` for the required retry window; the
default is 30 minutes. Memory budgets cover active requests, processing, and
database caching. The extension uses `fsync: false`; enable it to synchronize each write
when stronger protection against node or power failure is required.

To use memory queues instead, remove `exporters.otlp_grpc.sending_queue.storage`
and the `file_storage/queue` extension from each affected native Collector
configuration and from `service.extensions`. Replace `volumeClaimTemplates`
with `[]`, remove the queue mount, and keep the TLS mount. In your values file,
use `null` to remove inherited map entries. Decoded queued telemetry can occupy
several times its serialized size in memory.

Helm replaces lists in your values file as a whole. When overriding a list
such as `kubernetes.env`, `kubernetes.volumes`, or a receiver's
`scrape_configs`, copy the default list from `values.yaml` and change your copy.
Keep `GOMEMLIMIT` and `GOGC` in every replacement `env` list. The cluster event
reader also needs `POD_NAMESPACE`.

The watchdog, node collector, large-target pool, and nodeless pool require their Target
Allocators. Setting `targetAllocator.enabled: false` for any of them fails
rendering. Each allocator runs one non-root replica with a read-only root
filesystem, no privilege escalation, dropped capabilities, and `RuntimeDefault`
seccomp. Configure its resources and security under
`<component>.kubernetes.targetAllocator`, independently of the Collector.

### Resource defaults

| Collector | CPU request / limit | Memory request / limit | GOMEMLIMIT |
| --- | --- | --- | --- |
| Node | 500m / 2 | 1Gi / 2Gi | 768MiB |
| Metrics gateway | 250m / 6 | 512Mi / 4Gi | 1536MiB |
| Logs and traces gateways | 250m / 4 | 512Mi / 4Gi | 1536MiB |
| Large targets | 500m / 4 | 2Gi / 12Gi | 7168MiB |
| Nodeless targets | 100m / 1 | 256Mi / 1Gi | 384MiB |
| Events | 250m / 1 | 256Mi / 512Mi | 192MiB |
| Watchdog | 100m / 4 | 256Mi / 1Gi | 192MiB |

Node queues hold 64 MiB per signal, and the large-target queue
holds 128 MiB. Each has 32 consumers. Capacity includes in-flight payloads.

Gateways scale from 2 to 128 replicas at 60% CPU or 100% memory of Pod requests.
The large-target pool scales from 2 to 32 replicas at 75% CPU or 100% memory.
Watchdog scales from 2 to 16 replicas at 75% CPU or memory.
Gateways, the large-target pool and watchdog allow scale-up by the larger of
200% or 8 additional Pods per 15 seconds, with no stabilization window.
Scale-down uses Kubernetes defaults.

### Telemetry redaction

Enabled in the default Collector configurations in `values.yaml`.

- Masks recognized credentials in log bodies, including nested bodies, and in log attributes, metric data point attributes, span attributes and events, and resource/scope attributes. Uses `****`, including for quoted JSON and Basic/Bearer text. Preserves keys and counters such as `token_count`. No hashing.
- Runs last in the default source and gateway pipelines. Customize rules under `<workload>.otelcol.config.processors`, or disable redaction by removing its references from the relevant pipeline processor lists. Custom pipelines must include redaction explicitly.
- Basic matching does not cover OTLP `Metric.metadata`, exemplar `filtered_attributes`, span names, span link attributes, recursive key matching inside attribute maps and arrays, unknown credential names, or complex and encoded secrets. Attribute maps and arrays can still match value patterns through their text representation.
- Watchdog redacts other collectors' diagnostic exports (`service.telemetry`) because internal logs can include samples of rejected data. Sending these exports directly to a backend bypasses redaction.
- Watchdog's sidecar redacts watchdog's own logs, metrics, and traces, plus its own logs and metrics. Configure it under `watchdog.sidecar.otelcol.config`. Watchdog keeps `basic` tracing and the sidecar's own tracing stays disabled.
- Redaction changes exported copies. Original log files and container stdout, including `kubectl logs`, can still contain secrets.

Following OWASP logging guidance, exclude sensitive data from logs where possible.
Omit fields at the source when their names or presence are sensitive.

### Image registry

Set `global.imageRegistry` to override the image registry of all the components,
including the Collectors (`otelcol_image`) and the Target Allocators
(`targetallocator_image`). Caveat: `metrics-server` dependency, the nested OpenTelemetry Operator, and the
uninstall hook's kubectl image (`opentelemetry-kube-stack.cleanupJob.image`) do **not**
read this setting. Configure their image repositories explicitly when required.

### Cluster name

Set `global.clusterName` to add `k8s.cluster.name` to telemetry as it leaves
NKTP for the backend. Telemetry that already has `k8s.cluster.name` keeps it.
When `global.clusterName` is empty, the value is `unknown`.

## Before production

- **Disable the bundled LGTM backend.** It keeps data in memory, grants
  anonymous admin access to Grafana, and does not scale. Set
  `otel-lgtm.enabled: false`, then set your backend's endpoint in the
  `otlp_grpc` exporter of each gateway, the watchdog, and the watchdog's sidecar.
  Use `otlp_grpc` for gRPC or `otlp_http` for HTTP, matching your backend's supported
  ingestion protocol. When changing protocol, update each affected pipeline's
  `exporters` list too.
- **Secure backend export.** The default LGTM backend uses plaintext OTLP for
  local development. Enable TLS before sending gateway traffic
  to a production backend, and mount client certificates into the gateways if
  the backend requires mTLS.
- **Keep credentials in Secrets.** Add the Secret to the Collector's
  `kubernetes.env` and reference it from the exporter with `${env:NAME}`, for
  example in `headers`.
- **Provide the resource metrics API** so the gateways, large-target pool, and
  watchdog can autoscale.
- **Review resource requests and limits** under `<component>.kubernetes.resources`
  and `<component>.kubernetes.targetAllocator.resources`. Autoscaling targets
  are relative to requests.

## Bundled dependencies

| Chart | Version | Installs | Enabled by default | Values key |
| --- | --- | --- | --- | --- |
| opentelemetry-kube-stack | 0.25.1 | OpenTelemetry Operator chart 0.126.0, Operator 0.161.0, and CRDs | Yes | `opentelemetry-kube-stack` |
| prometheus-node-exporter | 4.56.1 | node-exporter | Yes | `node-exporter` |
| metrics-server | 3.13.1 | metrics-server 0.8.1 | No | `metrics-server` |
| prometheus-blackbox-exporter | 11.20.2 | blackbox_exporter 0.29.1 | No | `blackbox-exporter` |

The Collector, Target Allocator, kube-state-metrics, and LGTM images are set in
`values.yaml` under `otelcol_image`, `targetallocator_image`,
`kube-state-metrics.image`, and `otel-lgtm.image`.

## Developing the chart

| Path | Contents |
| --- | --- |
| `values.yaml` | Defaults, including every Collector configuration |
| `templates/` | One template per component, plus RBAC and certificates |
| `tests/` | helm-unittest suites and the default-output snapshot |
| `dashboards/` | Grafana dashboards provisioned into LGTM, and examples |

Run the chart tests from the repository root:

```bash
task test          # helm-unittest suites and snapshot
task test -- -u    # regenerate the snapshot after an intentional change
helm lint nktp && helm template nktp nktp
task dev:up        # Kind cluster with NKTP under Tilt
```

In a checkout, `CONTRIBUTING.md` covers setup, checks, and pull requests, and
`dev/README.md` explains the local loop. When a change alters a component's
behavior or values, update its page under `docs/components/` in the same pull
request.

This README is packaged into the chart, where links to other files do not
resolve. Keep it self-contained: it must not link to other documentation,
except the link below.

## Notice and disclaimer

**NOTICE AND DISCLAIMER:** This software automatically retrieves, accesses or
interacts with external materials. Those retrieved materials are not distributed
with this software and are governed solely by separate terms, conditions and
licenses. You are solely responsible for finding, reviewing and complying with
all applicable terms, conditions, and licenses, and for verifying the security,
integrity and suitability of any retrieved materials for your specific use case.
This software is provided "AS IS", without warranty of any kind. The author
makes no representations or warranties regarding any retrieved materials, and
assumes no liability for any losses, damages, liabilities or legal consequences
from your use or inability to use this software or any retrieved materials. Use
this software and the retrieved materials at your own risk.

## More information

The [NKTP documentation](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/tree/main/docs)
covers each component, configuration guides, upgrade notes, operations, and
design documents.
