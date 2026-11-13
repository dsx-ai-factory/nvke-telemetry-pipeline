# Scale tests

Run the mixed workload to exercise NKTP's telemetry paths together. The smaller
examples demonstrate individual generators and collection paths. Each directory
contains its plan, usage notes and any required Helm values.

| Workload | Purpose |
| --- | --- |
| [Mixed workload demo](mixed-workload-demo/README.md) | Small mixed workload with stock LGTM |
| [Mixed workload](mixed-workload/README.md) | Metrics, stdout logs, traces, Events and large scrape targets together |
| [Backend outage](backend-outage/README.md) | Simulate a backend outage to tune the gateway sending queue. Compare delivery, client backpressure, memory usage, and recovery |
| [Telemetrygen](examples/telemetrygen/README.md) | Send OTLP metrics, logs and traces |
| [Avalanche](examples/avalanche/README.md) | Generate Prometheus scrape targets |
| [KWOK](examples/kwok/README.md) | Simulate Kubernetes nodes and Pod lifecycles |
| [Pod churn](examples/pod-churn/README.md) | Create and replace real Pods with Jobs |
| [Stdout logs](examples/stdout-logs/README.md) | Generate container logs with Vector |
| [Kubernetes Events](examples/kube-events/README.md) | Generate paced Event updates with k6 |
| [Watchdog](examples/watchdog/README.md) | Send collector observations through watchdog |

See the [framework overview and annotated plan](reference.md#framework-overview)
to write or adapt plans.

## Setup

Requires an existing Kubernetes cluster, kubectl, Helm 4, Task and Go.
Use real worker and system pools sized for the workload. Record their node
counts, sizes and placement with benchmark results. Cluster provisioning and
deletion are managed outside this suite. Run commands from the repository
root. Examples expect the `nktp` release in namespace `nktp`.

Start each independent scale run with a clean test environment and an empty
telemetry backend. Collector self-telemetry is not automatically scoped by the
run ID. With LGTM, recreate its storage between runs. Apply the same expectation
to any replacement backend by using an empty, isolated data scope for all test
telemetry.

Authenticate with your cluster provider, then set `KUBECONFIG` to one readable
file with a current context. Edit the path before running:

```sh
export KUBECONFIG=/path/to/kubeconfig
```

`scale:install` installs cert-manager and NKTP in the selected cluster, waits for
readiness, then forwards [Prometheus](http://127.0.0.1:9090) and
[Grafana](http://127.0.0.1:3000). Keep these local ports free.
Use `task scale:forward:up` and `task scale:forward:down` to manage forwarding
without reinstalling.

## Run a workload

Choose a directory from the table and follow its capacity and setup notes. For
example:

```sh
task scale:install
task scale:test -- -plan "$PWD/tests/scale/examples/telemetrygen/plan.yaml"
```

A plan path is required. Installation creates or upgrades the `nktp` Helm release.
When a workload supplies values, pass its `HELM_VALUES_FILE` on every installation
to retain them. Relative values paths resolve from the repository root. Omitting
the file restores chart defaults and removes resources from the previous profile's
`extraObjects`. For an existing cluster, configure reuse of compatible shared
components in the values file.

`INSTALL_KWOK` defaults to `false`. Set it to `true` on installation and
uninstallation only for workloads that need simulated Kubernetes objects.
The [KWOK policies](environment/kwok/stages) belong to environment setup.
Simulated nodes do not represent physical benchmark capacity.

Set `KUBECONFIG` for installation, workload tests, outage tests, forwarding and
uninstallation. There is no default kubeconfig. Tests use the file's selected
context.
Relative Task paths resolve from the repository root. Paths passed with `-plan`
and `-kubeconfig` after `--` resolve from `tests/scale` for `scale:test` and
`tests/scale/backend-outage` for `scale:outage`.

Direct `go test` runs resolve relative `KUBECONFIG` values from the package
directory, `tests/scale` or `tests/scale/backend-outage`. Use an absolute
`KUBECONFIG` for direct runs.

For longer plans, set `TEST_TIMEOUT=30m` before `--`, allowing time for the plan
and cleanup. The default is 12 minutes. The command prints the run ID, generator
outcomes and measurement reports. Queries report observations, while configured
checks can fail a plan or complete a stage.

## Cleanup

Each run removes the workloads it creates, including after failure or
cancellation. The installed environment remains available for inspection until
you remove it. Save reports and observations before cleanup, then remove the test
environment and its backend telemetry before the next independent run. LGTM
storage is temporary. For another backend, also clear the isolated test data.

For the telemetrygen setup above:

```sh
task scale:uninstall
```

`scale:uninstall` removes NKTP and stops its forwards. Use the same `INSTALL_KWOK`
value as installation. With `true`, it also removes the KWOK releases and
completion policy. It preserves cert-manager, the cluster and kubeconfig.

`scale:uninstall` retains the watchdog and gateway queue PVCs. Before the
next independent run, follow the
[queue-storage deletion procedure](../../docs/guides/configure_exporter_queue_storage.md#delete-the-claims)
to prevent queued telemetry from being replayed. If the backing volumes
use the `Retain` reclaim policy, clean up their retained data as well.

Failed setup leaves resources for inspection. Uninstall does not run
automatically after a test.
