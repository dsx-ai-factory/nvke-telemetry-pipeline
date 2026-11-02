# Integration Tests

Tests of interactions between NKTP components, local processes, or test services without provisioning a Kubernetes cluster.

## Goals

- Validate component boundaries and assembled configurations beyond isolated unit behavior.
- Detect compatibility and lifecycle failures while remaining faster and simpler than end-to-end tests.

## Constraints

- May use ephemeral resources on the local machine, including loopback services and fake or local API servers.
- Must not require Kind, an existing Kubernetes cluster, or access to remote services.
- Must create, isolate, and clean up every resource they use.

## Telemetry redaction

With Docker running, build chart dependencies and pull the chart's Collector image
before running the test. From the repository root:

```bash
helm dependency build nktp --skip-refresh
docker pull otel/opentelemetry-collector-k8s:0.164.0
cd tests
go test -tags integration -count=1 -v ./integration
```

Collector tests require Docker and the pinned image, otherwise they report a skip.
