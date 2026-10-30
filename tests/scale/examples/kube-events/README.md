# Kubernetes Events with k6

One k6 Pod creates 100 owned Events with 1 KiB messages, then offers 10 PATCH
requests/s for three minutes. It deletes the Events on completion; Pod ownership
provides garbage-collection fallback on interruption.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install \
  HELM_VALUES_FILE=tests/scale/examples/kube-events/values.yaml
task scale:test TEST_TIMEOUT=15m -- \
  -plan "$PWD/tests/scale/examples/kube-events/plan.yaml"
```

[values.yaml](values.yaml) supplies the script and namespace-scoped RBAC.
The report shows successful/failed patches, dropped iterations, latency and Event
collector acceptance. k6 writes its own counters to bundled Prometheus. NKTP’s
default Event deduplication can coalesce updates; offered patches are not the
number of exported log records.

Edit `count`, `rate` and `duration` in `events.js` inside the values file. Increase
plan, Pod and Go timeouts together for longer runs. k6 fails on API errors or
dropped iterations, and cleans up partially created Events if setup fails.

See [shared cleanup](../../README.md#cleanup) to remove the environment.
