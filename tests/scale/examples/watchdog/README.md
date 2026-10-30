# Watchdog sources

One StatefulSet runs three source Pods for three minutes after readiness.
Each sends 100 metric points and 100 log records per minute, with 1 KiB log bodies,
directly to watchdog over mTLS on port 4317.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install \
  HELM_VALUES_FILE=tests/scale/examples/watchdog/values.yaml
task scale:test -- \
  -plan "$PWD/tests/scale/examples/watchdog/plan.yaml"
```

The report observes source identities, watchdog throughput, memory and queues;
telemetry remains available in bundled LGTM. The chart-issued
`nktp-daemonset-client-tls` Secret must exist in `nktp`.

Change `replicas`, `duration` and telemetrygen `--rate` in [plan.yaml](plan.yaml).
Adjust body size in [values.yaml](values.yaml). Each Pod’s UID distinguishes its
source, and the framework stops the long-running clients during cleanup.

See [shared cleanup](../../README.md#cleanup) to remove the environment.
