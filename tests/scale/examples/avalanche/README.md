# Avalanche

Two StatefulSets each run one Avalanche target for three minutes after readiness,
with 1,000 generated gauge series per target. One is scraped by its node collector;
the other by the large-target pool.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install \
  HELM_VALUES_FILE=tests/scale/examples/avalanche/values.yaml
task scale:test -- \
  -plan "$PWD/tests/scale/examples/avalanche/plan.yaml"
```

[values.yaml](values.yaml) supplies two PodMonitors. The
`nktp.nvidia.io/large-target: "true"` monitor label selects the large-target pool;
omitting it selects the node-local route. Routing is independent of target size.
Both routes retain telemetry in bundled LGTM.

The report shows scrape health and sample counts by `scale_route`. Change
`replicas`, `duration`, `--gauge-metric-count` or `--series-count` in
[plan.yaml](plan.yaml) to adjust load.

See [shared cleanup](../../README.md#cleanup) to remove the environment.
