# Telemetrygen

Three Jobs send metrics, logs and traces over mTLS to the corresponding NKTP
gateways for three minutes, using one worker at `--rate=10` per Job.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install
task scale:test -- \
  -plan "$PWD/tests/scale/examples/telemetrygen/plan.yaml"
```

The metrics Job continuously creates new series at a small rate. `active_series`
selects the current run in Prometheus; inspect logs and traces in Grafana with the
printed `test_run_id`. Each Pod has its own source identity.

Change `--rate`, `--workers` and `--duration` in [plan.yaml](plan.yaml) to adjust
load. Use `--unique-timeseries=false` for stable metric identities. The chart-issued
`nktp-daemonset-client-tls` Secret must exist in `nktp`.

See [shared cleanup](../../README.md#cleanup) to remove the environment.
