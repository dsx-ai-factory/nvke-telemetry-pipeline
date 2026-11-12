# Mixed workload demo

[Plan](plan.yaml) · [Profile](values.yaml)

Exercise node, large-target, nodeless, and Event collection through the gateways
with stock LGTM. The gateways send workload telemetry directly to LGTM.
NKTP self-telemetry keeps its watchdog path. k6 sends its observations directly
to LGTM.

## Load

Each generator runs one Pod. All generators start together without a ramp.

| Generator | Load |
| --- | --- |
| Small scraped | 200 series and one 100 B stdout record/s |
| Small instrumented | One OTLP metric point/s, one 100 B stdout record/s, two 1 KiB spans/s |
| Node-local target | 500 scraped series |
| Large logs | One 64 KiB stdout record every 5s |
| Large traces | One 64 KiB span/s |
| Events | One Event updated approximately once per second |
| Large-target pool | One small target with 150 series |
| Nodeless | One fixed target with 100 series |

The eight source Pods request **240m CPU and 672 MiB memory** in total.
They generate **950 scraped series and one OTLP series**, plus exporter and platform
metrics. Reserve additional capacity for the default NKTP topology and LGTM.
LGTM retains its chart defaults, including no explicit resource requests or
limits. The large-target monitor explicitly assigns the small target to that pool.

## Run

Follow the [shared setup](../README.md#setup), including a clean telemetry backend.
Use a small real cluster with a healthy Metrics Server for the default HPAs.
Reuse an existing node exporter if one already binds port 9100. This profile
does not require special node labels or a dedicated node per generator.

The cluster must already have an SSD-backed default StorageClass for the
watchdog and gateway queues.

Run from the repository root. Edit the kubeconfig path and confirm its selected
context before installation.

```sh
export KUBECONFIG=/path/to/kubeconfig
kubectl --kubeconfig "$KUBECONFIG" config current-context
task scale:install \
  HELM_VALUES_FILE=tests/scale/mixed-workload-demo/values.yaml
kubectl --kubeconfig "$KUBECONFIG" -n nktp get pvc
RUN_ID="demo-$(date -u +%Y%m%d%H%M%S)"
task scale:test TEST_TIMEOUT=25m -- \
  -plan "$PWD/tests/scale/mixed-workload-demo/plan.yaml" -arg run-id="$RUN_ID"
```

The queue PVCs should be `Bound`.

## Verify

The `ready` check requires all seven run-owned Pods, four fresh scrape targets,
a fresh OTLP metric in LGTM, three active stdout emitters, successful Event updates, and
successful gateway exports of metrics, logs, and spans to LGTM. It must hold
for two minutes. Pod readiness uses NKTP's kube-state-metrics job,
`nktp/nktp-ksm-pods`, so other kube-state-metrics installations do not inflate
the Pod count.
Missing or stale required data prevents completion. The observation deadline is
12 minutes, the plan ceiling is 15 minutes, and the outer timeout allows cleanup.

Confirm positive rates for these components in the three printed queries:

| Query | Expected components |
| --- | --- |
| `sent_metric_points` | daemonset, large-target-metrics, nodeless-metrics, gateway-metrics, watchdog |
| `sent_log_records` | daemonset, cluster-events, gateway-logs |
| `sent_spans` | daemonset, gateway-traces |

Component jobs have the `nktp-` prefix. Inspect the NKTP Health dashboard at
[Grafana](http://127.0.0.1:3000) for restarts, queue growth, and export errors.
These are activity checks. Aggregate Event and export counters include
background traffic and do not prove per-record delivery or durability.

Save the report before cleanup. The runner removes its seven Pods. The nodeless
target and LGTM remain until uninstall. Uninstall clears LGTM data but retains
the watchdog and gateway queue PVCs.

```sh
task scale:uninstall
```

After uninstall, [delete the retained queue claims](../../../docs/guides/configure_exporter_queue_storage.md#delete-the-claims)
before the next independent run so queued telemetry is not replayed into the new backend.
