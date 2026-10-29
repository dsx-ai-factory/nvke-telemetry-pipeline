# Scale tests

Run the mixed workload to exercise NKTP's telemetry paths together. The smaller
examples demonstrate individual generators and collection paths. Each directory
contains its plan, usage notes and any required Helm values.

| Workload | Purpose |
| --- | --- |
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

Requires kubectl, Helm 3 or 4, Task and Go. Local provisioning also requires
Docker and Kind. Run commands from the repository root. Examples expect the
`nktp` release in namespace `nktp`.

Create a local cluster with the [Kind configuration](environment/kind-config.yaml):

```sh
export KUBECONFIG="$(task --silent scale:up)"
```

For an existing cluster, authenticate with its provider, then edit the absolute
kubeconfig path below:

```sh
export KUBECONFIG=/path/to/kubeconfig
```

`scale:up` only provisions the local cluster and writes `tests/scale/.kubeconfig`.
`scale:install` installs cert-manager, KWOK and NKTP in the selected cluster,
waits for readiness, then forwards [Prometheus](http://127.0.0.1:9090) and
[Grafana](http://127.0.0.1:3000). Keep these local ports free.

## Run a workload

Choose a directory from the table and follow its capacity and setup notes. For
example:

```sh
task scale:install INSTALL_KWOK=false
task scale:test -- -plan "$PWD/tests/scale/examples/telemetrygen/plan.yaml"
```

A plan path is required. Installation creates or upgrades the `nktp` Helm release.
When a workload supplies values, pass its `HELM_VALUES_FILE` on every installation
to retain them. Relative values paths resolve from the repository root. Omitting
the file restores chart defaults and removes resources from the previous profile's
`extraObjects`. For an existing cluster, configure reuse of compatible shared
components in the values file.

`INSTALL_KWOK` defaults to `true`. Set it to `false` on both installation and
uninstallation when KWOK is shared or not needed. The
[KWOK policies](environment/kwok/stages) belong to environment setup.

Tests use the selected kubeconfig's current context. Without an override,
`scale:test` uses `tests/scale/.kubeconfig`. Passing
`KUBECONFIG="$KUBECONFIG"` as a Task variable selects the file set above. Relative Task
paths resolve from the repository root, while `-plan` and `-kubeconfig` paths
passed after `--` to `scale:test` resolve from `tests/scale`.
Prefer absolute paths.

For longer plans, set `TEST_TIMEOUT=30m` before `--`, allowing time for the plan
and cleanup. The default is 12 minutes. The command prints the run ID, generator
outcomes and measurement reports. Queries report observations, while configured
checks can fail a plan or complete a stage.

## Cleanup

Each run removes the workloads it creates, including after failure or
cancellation. The installed environment remains available for inspection and
subsequent runs. Save observations before removing it because LGTM storage is
temporary.

For the telemetrygen setup above:

```sh
task scale:uninstall INSTALL_KWOK=false
task scale:down
```

`scale:uninstall` removes NKTP and stops its forwards. Use the same `INSTALL_KWOK`
value as installation. With `true`, it also removes the KWOK releases and
completion policy. It preserves cert-manager, the cluster and kubeconfig.

`scale:down` deletes only the local scale Kind cluster and its kubeconfig, even
when another `KUBECONFIG` is exported. Failed setup leaves resources for
inspection. Neither cleanup command runs automatically after a test.
