# End-to-End Tests

Tests of user-visible NKTP behavior after deploying the real chart to a Kind or explicitly provided Kubernetes cluster.

## Goals

- Validate installation, upgrades, removal, topology, dependencies, and Kubernetes API behavior.
- Verify complete telemetry paths where a scenario requires them.

## Running

Requires a Docker-compatible container runtime, [Kind](https://kind.sigs.k8s.io/),
[Task](https://taskfile.dev/installation/), Helm, kubectl, and Go. From the
repository root:

```sh
task e2e:up      # create the cluster and fetch the chart dependencies
task e2e:test    # run the suite, as often as you like
task e2e:down    # delete the cluster
```

## Flags

`task e2e:test` passes these suite flags:

| Flag | Purpose | Default |
| --- | --- | --- |
| `-kubeconfig` | Cluster kubeconfig; specify explicitly to avoid using another context | `tests/e2e/.kubeconfig` via Task |
| `-chart` | Chart directory | `nktp/` |
| `-namespace` | Release namespace | `nktp` |
| `-release-name` | Helm release to install or reuse | `nktp-e2e` |
| `-values` | E2E values file | `tests/e2e/values.yaml` |
| `-keep-release` | Reuse a deployed release if present and keep the release after testing | `false` |

To test a different chart, edit the absolute chart path below:

```sh
E2E_CHART=/path/to/chart
task e2e:test -- -chart "$E2E_CHART"
```

With `-keep-release`, the deployed release selected by `-release-name` in
`-namespace` is reused, skipping installation from `-chart` and `-values`.
If that release does not exist, the suite installs it using those inputs and
keeps it after testing. Chart reapplication checks still use `-chart` and
`-values`, even when installation is skipped.

To reuse an existing installation, edit the kubeconfig path and release name:

```sh
E2E_KUBECONFIG=/path/to/cluster.kubeconfig
E2E_RELEASE=nktp

task e2e:test KUBECONFIG="$E2E_KUBECONFIG" -- \
  -release-name "$E2E_RELEASE" -keep-release
```

By default, the suite replaces its existing release and removes it after testing;
point it only at a disposable cluster.

## Adding a new test

1. Define a single `feature` per Test function.
2. If the test requires fixtures, add the necessary resources under `tests/e2e/manifests/<test-name>/`. Avoid using fixtures in directories other than the one you create for this test.
3. Try reusing existing helpers from `tests/internal/*` if possible, or create new ones if necessary.
4. Use the Fluent APIs to define features: `features.NewWithDescription(...).WithSetup(...).AssessWithDescription(...).WithTeardown(...)`
5. Prefer the usage of `WithSetup` and `WithTeardown` to their unnamed counterparts (`Setup` and `Teardown`). 
6. For the descriptions, use concise sentences in `CamelCase` e.g. `WithSetup("GeneratingMetricQueries")`
7. Avoid using the available context to pass data between steps. Prefer test-level variables.
8. Prefer using the established `Setup->Assess->Teardown` test flow and avoid using `t.Cleanup` to register clean up callbacks.
9. Prefer adding a short description in the `features.NewWithDescription("...")` rather than above the test function.
