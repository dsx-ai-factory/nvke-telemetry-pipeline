# KWOK

Create two emulated nodes and 20 Pods concurrently. Pods select only this run’s
nodes and complete after about 30 seconds of simulated Running time. Nodes remain
for three minutes, then the framework removes them.

Follow the [shared setup](../../README.md#setup), set `KUBECONFIG`, then run
from the repository root:

```sh
task scale:install INSTALL_KWOK=true
task scale:test -- \
  -plan "$PWD/tests/scale/examples/kwok/plan.yaml"
```

The installer supplies KWOK and its completion policy. KWOK simulates the Pods;
no workload image runs and the declared CPU and memory are simulated capacity.

Change the node-group `replicas`, Pod `count` or node `duration` in
[plan.yaml](plan.yaml). Keep nodes alive long enough for scheduling and completion.

Use `task scale:uninstall INSTALL_KWOK=true` for [cleanup](../../README.md#cleanup).
