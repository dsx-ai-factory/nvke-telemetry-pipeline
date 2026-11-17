# Integration Tests

Tests of interactions between NKTP components, local processes, or test services without provisioning a Kubernetes cluster.

The NKTP Chart CI workflow runs this suite on pull requests that change the chart,
integration tests, or shared test dependencies.

## Goals

- Validate component boundaries and assembled configurations beyond isolated unit behavior.
- Detect compatibility and lifecycle failures while remaining faster and simpler than end-to-end tests.

## Constraints

- May use ephemeral resources on the local machine, including loopback services and fake or local API servers.
- Must not require Kind, an existing Kubernetes cluster, or access to remote services.
- Must create, isolate, and clean up every resource they use.

## Run the suite

Run the commands in this section from the repository root. Register the chart
repositories, build the chart dependencies, then run the tests. This needs
Helm, Go, and network access:

```bash
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts --force-update
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update
helm dependency build nktp --skip-refresh
go -C tests test -tags integration -count=1 -v ./integration
```

The telemetry redaction tests run the chart's Collector in Docker. They skip
when Docker or the Collector image is unavailable. To include them, start
Docker and pull the image that `otelcol_image` names in `nktp/values.yaml`
before running the tests. This needs `yq`:

```bash
docker pull "$(yq -r '.otelcol_image | .registry + "/" + .repository + ":" + .tag' nktp/values.yaml)"
```

If you set `global.imageRegistry` or `watchdog.otelcol.image` in
`nktp/values.yaml`, pull the image that the skipped test names instead.

CI sets `NKTP_REQUIRE_COLLECTOR=true` to fail instead of skipping.
