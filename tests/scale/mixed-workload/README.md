# Mixed workload

- [Plan](plan.yaml) · [Profile](values.yaml) · [Load model](busy-node.md)
- Load: 100k series and about 4 GiB/hour each of logs and spans per worker
- Paths: node, Events, large-target and nodeless collectors → gateways → blackhole
- NKTP: chart resource and autoscaling defaults, with mTLS for collector-to-gateway traffic
- Observations: blackhole self-telemetry and OTLP adapter observations and
  self-telemetry go directly to LGTM over plaintext OTLP/gRPC. NKTP
  self-telemetry keeps its watchdog path.

The node collector, gateways, large-target and nodeless pools, and Event reader
use detailed self-telemetry metrics and unsampled internal traces for retry
measurements. Blackhole also uses detailed self-telemetry metrics. Stored traces
retain the chart sampling policy.

## Load

The plan runs nine workload groups in four creation cohorts within one stage,
alongside a shared Prometheus measurement. A Go template loop keeps each group's
Pod template in one place. Generator `start_delay` values schedule creation at
zero, five, ten and fifteen minutes from stage start, reaching about 1%, 25%,
50% and 100% of the final Pod count. Earlier cohorts keep running as later ones
start. This spreads Pod creation and its infrastructure telemetry across the
ramp, giving collectors time to scale with the synthetic load.

Each 24-target large group and the six-target group start one target, their
smallest nonzero cohort. The initial cohort has 104 Pods (about 1% of the total).
The large targets make its share of telemetry volume higher. The full workload
remains 10,154 Pods, including 100 Event producers. Scheduling and image pulls
can extend startup beyond each creation delay.

| Creation delay | New Pods | Cumulative Pods |
| --- | ---: | ---: |
| 0 minutes | 104 | 104 |
| 5 minutes | 2,435 | 2,539 |
| 10 minutes | 2,539 | 5,078 |
| 15 minutes | 5,076 | 10,154 |

Each of 100 workers runs 100 local generator Pods:

| Group | Pods/node | Metrics/Pod | Logs/Pod | Spans/Pod |
| --- | ---: | --- | --- | --- |
| Small, scraped | 50 | 200 scraped series | 100 B × 16/s | — |
| Small, instrumented | 40 | 250 series over OTLP | 100 B × 16/s | 1 KiB × 16/s |
| Node-local targets | 4 | 20,000 scraped series | — | — |
| Large logs | 4 | — | 64 KiB × 4/s | — |
| Large traces | 2 | — | — | 64 KiB × 4/s |

| Signal | Per node | Across 100 workers |
| --- | ---: | ---: |
| Synthetic series | 90k scraped + 10k OTLP | 10M |
| Log records | 1,456/s | 145,600/s |
| Log bodies | 1,192,576 B/s | 119,257,600 B/s |
| Spans | 648/s | 64,800/s |
| Span payloads | 1,179,648 B/s | 117,964,800 B/s |

- Metrics: Avalanche, four generated labels, no churn, 30s scrapes / 10s timeout. Instrumented Pods use private Avalanche targets and an OTLP adapter.
- Logs: Vector `demo_logs` → stdout → runtime files → native file_log receiver.
- Adjust load: Pod counts and Avalanche arguments in the plan, log/span sizes and rates in the profile ConfigMaps. Rate calculations are beside each value. Span rates count spans, with two spans per trace.
- Bytes: log bodies and span payload attributes. Newline, CRI and OTLP overhead are additional. Platform telemetry adds background load.

The large-target collector pool handles these additional targets at **60s / 55s**:

| Series/target | Pods | Total series |
| ---: | ---: | ---: |
| 150,000 | 24 | 3,600,000 |
| 300,000 | 24 | 7,200,000 |
| 600,000 | 6 | 3,600,000 |
| Total | 54 | 14,400,000 |

One k6 Pod per worker creates 100 Events with 1 KiB messages, then
updates them at 10/s for about 90 minutes: **1,000 updates/s** across 10,000 objects.
The [Kubernetes Events example](../examples/kube-events/README.md) demonstrates this generator.
The run-scoped workload has 24.4M synthetic series.

The profile also installs one fixed Avalanche target with 100 synthetic series,
scraped every 30s through a static `ScrapeConfig`. Its Service address has no
node discovery labels, so the nodeless pool owns it. The target adds a 10m CPU
and 32Mi memory request on the system pool. It runs from profile installation
until `task scale:uninstall`, independently of the plan and run ID.

## Cluster

| Pool | Nodes | Node size | Required label |
| --- | ---: | --- | --- |
| Generators | 100 | 16 vCPU / 32 GiB | `nodeGroup=customer-cpu` |
| NKTP and support | 8 | 64 vCPU / 128 GiB | `nodeGroup=system-cpu` |

- Setup: [shared prerequisites](../README.md#setup), existing fleet, healthy Metrics Server for HPA. Workers must reach LGTM on port 4317 for OTLP adapter telemetry and port 9090 for k6 metrics.
- Placement: 101 synthetic Pods on 46 workers and 102 on 54 workers, plus platform services. Reserve Pod slots and addresses.
- Blackhole and bundled LGTM: 72 CPU / 80 GiB requested, plus capacity for NKTP and HPA.
- Profile: adapt placement and API access settings to your cluster. Keep an untainted node for Helm cleanup. Set `CERT_MANAGER_VALUES_FILE` if needed.

## Run

Stop other generators and install the adapted profile, including its `extraObjects`.

Edit the absolute kubeconfig path below before running these commands.

```sh
export KUBECONFIG=/path/to/kubeconfig
task scale:install \
  HELM_VALUES_FILE=tests/scale/mixed-workload/values.yaml
RUN_ID="mixed-$(date -u +%Y%m%d%H%M%S)"
task scale:test TEST_TIMEOUT=130m -- \
  -plan "$PWD/tests/scale/mixed-workload/plan.yaml" -arg run-id="$RUN_ID"
```

## Results

- Window: the delivery checks run concurrently and must pass together for 30 consecutive minutes while load is active.
- Transport rates: metrics, logs and traces use ten-minute windows with 1% tolerance,
  at least nine counter samples and 90-second freshness. The longer history reduces
  timing skew and takes longer to settle after scaling or counter resets. Scrape-pool,
  Event-reader and per-worker liveness checks retain five-minute windows.
- Runtime: qualification stops the generators and waits for cleanup. A generator ending before qualification or an unqualified 115-minute observation window fails the run. Generator limits remain 90 minutes, with each StatefulSet timed from its own readiness and a 120-minute plan ceiling.
- Load: targets, series, offered stdout records and body bytes, native receipt and export rates, serialized OTLP bytes and aggregate Event acceptance. Compare rates over the same time window.
- Performance: CPU/memory use, component requests and limits, observed peaks, throttling, queues, HPA replicas and bounds, and RPC call rates and p95 latency.
- Retries: workload Collector export retries by component, Pod, exporter and signal. Counts arrive after export completion and can be incomplete if self-telemetry is lost or truncated.
- Health: readiness, restarts, termination reasons, refusals, export failures and observation freshness. Error counters can be absent when no errors occurred.
- Events: require 10 successful updates/s per producer and 1,000/s across 100 workers, with no failed requests or dropped iterations. Exclude seeding and cleanup.
- Nodeless: expect one fresh target for `scale_workload="mixed-workload-nodeless"`
  with `nktp_metrics_pool="nodeless"` in `fresh_targets`. `target_series` and
  `scrape_duration` include the same fixed target without a run-ID filter.
  Check both collector replicas and the allocator in the component observations.
  Only the replica assigned the target needs to show scrape activity.
- Retain LGTM and the dashboard for review before [environment cleanup](../README.md#cleanup).

Pod inventory and health queries select NKTP's `nktp/nktp-ksm-pods` and
`nktp/nktp-ksm-unscheduled-pods` jobs. This excludes duplicate observations from
other kube-state-metrics installations while retaining pending Pod diagnostics.
HPA queries select `nktp/nktp-ksm-workloads`.

Node counts include background traffic. Log body bytes measure generated load.
Span payload bytes are configured load, while serialized OTLP bytes include
encoding overhead.

## Limitations

- Passing confirms transport consistency during the observation window. Verify designed load and resource use separately.
- Delivery checks do not prove lossless processing, exactly-once delivery or delivery after load stops.
- Event acceptance includes cluster-wide background traffic, which can mask generated Event losses.
- Check 64 KiB log reconstruction and rotation. Source scrape health alone does not establish OTLP delivery.
- Repetitive payloads compress unusually well. Blackhole cannot verify durable delivery. LGTM storage is temporary.
