# Pod churn

Two Jobs each run two Pods concurrently and finish after six completions.
Each real Pod sleeps for 30 seconds: up to four run at once and 12 run in total.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install \
  HELM_VALUES_FILE=tests/scale/examples/pod-churn/values.yaml
task scale:test -- \
  -plan "$PWD/tests/scale/examples/pod-churn/plan.yaml"
```

The report observes Pod phases and placement through kube-state-metrics.
[values.yaml](values.yaml) exposes the run label for attribution. Short-lived
states can fall between scrapes.

Change `count`, `parallelism`, `completions` and the sleep duration in
[plan.yaml](plan.yaml). Jobs and their Pods are removed after completion.

See [shared cleanup](../../README.md#cleanup) to remove the environment.
