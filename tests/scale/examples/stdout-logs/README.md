# Vector stdout logs

Two StatefulSets each run one Vector Pod for three minutes after readiness.
One emits 100-byte bodies at 16 records/s; the other emits 64 KiB bodies at
4 records/s. Logs follow runtime files → node collectors → logs gateway → LGTM.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install \
  HELM_VALUES_FILE=tests/scale/examples/stdout-logs/values.yaml
task scale:test -- \
  -plan "$PWD/tests/scale/examples/stdout-logs/plan.yaml"
```

The report compares emitted records and body bytes with node/gateway acceptance;
acceptance includes other cluster traffic. Inspect `vector-stdout` container logs
in Grafana to see the payloads.

Change `replicas` and `duration` in [plan.yaml](plan.yaml), and `interval` or
body size in the ConfigMaps in [values.yaml](values.yaml). Large records exercise
CRI reconstruction. Body byte rates exclude newline and transport overhead.

See [shared cleanup](../../README.md#cleanup) to remove the environment.
