# Simulate a backend outage to tune the gateway sending queue

This test checks metrics delivery and client backpressure during a backend
outage. It derives an isolated gateway from the installed
`nktp/nktp-gateway-metrics` and a scraper from
`nktp/nktp-large-target-metrics`. It scrapes synthetic metrics every 30s and takes
a verification backend down for one minute before measuring recovery. The
scrape templates are based on real-world samples of kube-state-metrics and
dcgm-exporter scrapes. The default mixed template combines 192 text bytes with
ten labels to exercise the higher memory cost of many short attributes; see
[Measure in-memory queue heap overhead](#measure-in-memory-queue-heap-overhead).

If your metrics differ from these scrape templates, supply a representative
`.prom` template, measure its overhead, and size the queue with room for baseline heap
and request processing. Then run the backend outage test to check delivery,
client backpressure, and overall memory usage with that queue size.

## Run

Preflight requires a healthy OpenTelemetry Operator in the gateway source
namespace before creating test resources.

Follow the [cluster setup](../README.md#setup) and stop other scale generators.
From the repository root:

```sh
task scale:outage KUBECONFIG="$PWD/tests/scale/.kubeconfig" -- \
  -outage-gateway-memory-limit 1Gi \
  -outage-gateway-queue-bytes 134217728 \
  -outage-scraper-memory-limit 8Gi \
  -outage-backend-memory-limit 1Gi \
  -outage-scrape-series 175000 -outage-scrape-interval 30s \
  -outage-duration 1m \
  -outage-output /tmp/nktp-outage-scrapes
```

Adjust `-outage-scrape-series` and `-outage-scrape-interval` to match your load.
The defaults produce roughly 5,833 points/s, arriving in 175,000-point scrape
bursts. A one-minute outage covers about two 30-second scrape cycles. With the
mixed template, this reached about 90% occupancy of the 128 MiB gateway queue
without refusals or failed RPCs in the measured run.

The test runs three phases:

1. **Baseline** (`-outage-baseline 1m`): scrape with the backend available to
   check that the pipeline handles the load before introducing an outage.
   Then pause input and wait for delivery and both queues to drain. The test
   stops if points are missing, duplicate receipts occur, or either Collector
   records refusals or exporter failures.
2. **Outage** (`-outage-duration 1m`): stop the backend and resume scraping.
   This measures how the scraper and gateway buffer data and whether clients
   experience backpressure. After one minute, pause input and let in-progress
   scrapes finish before restoring the backend.
3. **Recovery** (`-outage-recovery 3m`): once the backend is ready again, wait
   up to three minutes for outage data to arrive and both queues to drain.
   Input stays paused. The test reports recovery time and estimated missing
   or duplicate points, and finishes early when delivery and draining complete.

Baseline and outage durations control active scraping windows and must each
cover at least one scrape interval. Draining in-flight requests and restarting
the backend add elapsed time beyond those windows.

Select a template with `-outage-scrape-template`; paths are relative to
`tests/scale/backend-outage`, or absolute:

| Template | Text bytes/series¹ | Labels/series |
| --- | ---: | ---: |
| `metrics-ksm-mixed.prom` (default) | 192 | 10 |
| `metrics-ksm.prom` | 192 | 5 |
| `metrics-dcgm.prom` | 384 | 10 |

¹ Text bytes per sample; queue capacity counts serialized OTLP bytes.

Mixed combines KSM-sized samples with DCGM's label count and had the highest
measured heap overhead. All templates use stable series without churn;
`-outage-scrape-series` sets the total per scrape.

Custom templates can use `__SERIES__` for pod/UID identities and `__NODE__`
for node names; the test repeats samples to reach the requested count.

Use `-outage-gateway-source` / `-outage-gateway-source-namespace` and
`-outage-scraper-source` / `-outage-scraper-source-namespace` to override the
installed Collectors used as configuration sources: `nktp-gateway-metrics` for
the gateway and `nktp-large-target-metrics` for the scraper, both in `nktp`.

The scraper inherits processing, scrape limits, resources, GC, and exporter
settings from the selected large-target metrics Collector, with the test's
memory and queue overrides applied. For the chart's `k8s` image, it uses the
same-version `contrib` image to expose test counters; override with
`-outage-scraper-image` if needed.

### Choose queue and memory sizes

| Flag | Purpose | Default |
| --- | --- | --- |
| `-outage-gateway-memory-limit` | Gateway container limit | `1Gi` |
| `-outage-gateway-queue-percent` | Queue capacity as a percentage of the memory limit | Unset; uses queue bytes |
| `-outage-gateway-queue-bytes` | Absolute queue capacity | `134217728` (128 MiB) |
| `-outage-gateway-spike-percent` | Memory-limiter spike allowance as a percentage of container memory | Inherit installed gateway |
| `-outage-scraper-memory-limit` | Scraping Collector limit | `8Gi` |
| `-outage-scraper-queue-percent` | Scraper queue as a percentage of its memory limit | Inherit source ratio |
| `-outage-scraper-queue-bytes` | Absolute scraper queue capacity | Inherit installed large-target Collector |
| `-outage-scraper-disable-batch` | Disable scraper batch processors; retain exporter queue batching | `false` |
| `-outage-backend-memory-limit` | Verification backend limit | `1Gi` |

For each Collector, choose either queue bytes or a positive queue percentage.
A gateway percentage replaces the default byte capacity; do not also pass a
positive byte override. The default gateway queue stays at 128 MiB when memory
changes. Set `-outage-gateway-queue-bytes 0` to inherit the source queue-to-memory
ratio instead. The scraper inherits its source ratio unless overridden.
Passing `-outage-gateway-memory-limit ""` or `-outage-scraper-memory-limit ""`
uses the selected installed Collector's memory limit. Passing
`-outage-backend-memory-limit ""` uses the limit in the verification backend's
[Collector manifest](collectors.yaml), selected by `-outage-manifest`.

Queue capacity counts serialized bytes, not actual heap usage. The chart's
default memory limiter starts refusing at 55% of the container limit, so heap
overhead and processing buffers can cause backpressure before the queue fills.
Setting the queue to 55% does not reserve that memory or ensure it can be filled.
To compare spike allowances, add `-outage-gateway-spike-percent 16`; with an
80% hard limit, this sets the soft limit to 64%. The override must be a positive
integer below the hard limit and requires a percentage-based memory limiter.

Compare queue sizes with the same load, scrape interval, and memory limits.
Look for lower client RPC failures and faster recovery, alongside queue
occupancy and heap usage. Successful eventual delivery alone does not establish
that the gateway avoided backpressure: the scraper also buffers and retries.

### Read the results

<details>
<summary>Example normal-outage result (100,000 series per scrape)</summary>

Summary from `task scale:outage -- -outage-scrape-series 100000`, with a
one-minute outage. The artifact path is shortened.

```text
results: /tmp/outage-example/report.json
scraper (.resource_summary.scraper, .phase_metrics.*.scraper, .scraper_client_impact):
  peak heap 315.2 MiB; RSS 532.4 MiB; queue 32.8 MiB (12.8%)
  baseline: CPU 5.68s; RPCs 38, failed 0; mean 336.4ms, p95 ~485.6ms
  outage: CPU 4.98s; RPCs 38, failed 0; mean 323.7ms, p95 ~485.6ms
  recovery: CPU 0.50s; RPCs 0, failed 0
  refused 0 points
  backlog at backend restore: 0.0 MiB (.at_backend_restore_scraper_queue_bytes)
gateway (.resource_summary.gateway, .phase_metrics.*.gateway, .gateway_impact):
  peak heap 330.6 MiB; RSS 453.9 MiB; queue 65.6 MiB (51.3%)
  limiter: hard 80.0%; spike 25.0%; soft 55.0% (.gateway_memory_limiter)
  baseline: CPU 6.04s; RPCs 38, failed 0; mean 312.3ms, p95 ~485.2ms
  outage: CPU 6.17s; RPCs 38, failed 0; mean 293.6ms, p95 ~483.6ms
  recovery: CPU 0.49s; RPCs 0, failed 0
  refused 0 points; memory-limiter refused N/A; enqueue failed 0
delivery (.result): 2 scrapes; 200000 produced, 200000 received; estimated missing 0, duplicates 0; recovery 36.17s (.recovery_seconds)
delivery verification (.verification_complete): complete
combined sampled peak (.resource_summary.combined): heap 558.4 MiB; RSS 985.0 MiB
--- PASS: TestBackendOutage (216.08s)
PASS
```

</details>

**PASS** means the baseline was healthy, outage delivery recovered within the
recovery timeout with no detected missing points, and both sending queues
drained. **FAIL** means a setup, baseline, delivery, or verification check failed;
the failure message identifies which check.

A pass does not mean the gateway queue alone retained everything or that clients
avoided backpressure: the scraper also buffers and retries. When tuning, compare:

- **Delivery:** produced/received points and estimated loss or duplicates
  (`.result`).
- **Client impact:** failed RPCs and latency versus baseline, plus gateway
  refusals (`.scraper_client_impact`, `.phase_metrics`, `.gateway_impact`).
- **Memory:** scraper, gateway, and combined peak heap/RSS, alongside queue
  occupancy (`.resource_summary`).
- **Recovery:** time to deliver the backlog and drain both queues
  (`.recovery_seconds`).

Stdout includes these results and their paths in `report.json`. Delivery checks
count points per successful scrape, so partial loss and duplication within a
scrape can cancel out. Compare the same actual scrape counts and point volumes.

Generate a Markdown comparison from the repository root, editing the report
paths for your runs:

```sh
go -C tests run ./scale/backend-outage/cmd/compare-outage \
  /tmp/run-a/report.json /tmp/run-b/report.json
```

The table follows scraper → gateway → delivery, shows the recorded test
PASS/FAIL result and verification errors, and includes overhead estimates only
for queue-overhead runs.

Heaps are saved every 30s in `heaps/`; change `-outage-heap-interval`, or set it to
`0` to disable periodic captures. Names are
`<collector>-<UTC timestamp>-<phase>.heap.pprof`. Other artifacts are grouped in
`logs/`, `diagnostics/`, `manifests/`, and `prometheus-payload/`.

Temporary workloads are removed on completion, failure, or Ctrl+C.

## Measure persistent queue volume usage and I/O

Use `-outage-gateway-storage` to measure volume usage and gateway read/write
activity during normal operation, a backend outage, and recovery. It puts the
gateway queue on a PVC without changing its capacity. This requires outage
mode, a provisioning StorageClass, and kubeconfig permissions for `pods/exec`
and `nodes/proxy`.

After the [scale cluster setup](../README.md#setup), run from the repository
root. Edit the output directory and storage size for your experiment:

```sh
OUTPUT_DIR=/tmp/nktp-outage-volume
QUEUE_STORAGE=1Gi

task scale:outage KUBECONFIG="$PWD/tests/scale/.kubeconfig" -- \
  -outage-gateway-storage "$QUEUE_STORAGE" \
  -outage-output "$OUTPUT_DIR"
```

The claim uses the default StorageClass; add
`-outage-gateway-storage-class local-path` to select Kind's class explicitly.
Cleanup removes the claim, but a StorageClass with a `Retain` reclaim policy
leaves the PV for you to delete.

Read `.gateway_volume` in `report.json`:

- `peak_allocated_bytes` shows peak queue volume usage; `peak_growth_bytes`
  shows growth above the initial allocation. `samples` tracks usage over time.
  Apparent file size can differ from allocated volume space.
- `phases` shows average and peak sampled read/write bytes per second and
  IOPS per device. Compare these with queue occupancy and recovery time to
  assess storage demand. Recovery drains the backlog with input paused.
- Check `usage_errors`, `io_errors`, and `counter_resets` before using the
  measurements. Missing metrics are unavailable, not zero. If
  `io_activity_observed` is false, gateway I/O attribution remains unverified.
  A delivery PASS alone does not establish valid volume measurements.

I/O counters cover the gateway container, excluding other workloads' activity.
Other processes can still cause storage contention, and caching can hide physical
reads or delay writes. Treat the results as observations of this workload;
Kind local storage does not establish production-volume requirements.

## Measure in-memory queue heap overhead

A text sample such as `metric{label="value"} 1` does not occupy only that many
bytes in an in-memory sending queue. The queue counts serialized OTLP bytes,
while its decoded points, attributes, and strings consume heap. For the scrape
templates below, additional retained heap was at least twice the serialized
size; the scrape text size is not a queue memory budget.

The test's optional `-outage-mode queue-overhead` mode measures that multiplier
for your workload. Use a representative template to estimate serialized bytes
per point and heap cost, choose a queue that fits alongside baseline and
processing memory, then return to the backend outage test to validate its
effect on client backpressure.

Queue-overhead mode collects `-queue-overhead-scrapes` successful outage scrapes
(default `2`, excluding baseline), then stops scraping, waits for in-flight
requests to finish, forces GC, and measures the remaining live heap.
`-queue-overhead-settle-timeout` defaults to `1m`; it is the maximum wait for
in-flight requests to finish. If the wait times out, the estimate is marked
inconclusive. The backend is then restored and recovery runs.

`-outage-duration` applies only to outage mode. Explicitly combining it with
queue-overhead mode, or passing queue-overhead flags in outage mode, is rejected.

For the default ten-label, 192-byte text samples, start with a load that fits
comfortably in the queue. From the repository root, choose a fresh output path:

```sh
OUTPUT_DIR=/tmp/nktp-queue-overhead-192b-10labels

task scale:outage KUBECONFIG="$PWD/tests/scale/.kubeconfig" -- \
  -outage-mode queue-overhead \
  -queue-overhead-scrapes 2 \
  -queue-overhead-settle-timeout 1m \
  -outage-gateway-memory-limit 1Gi \
  -outage-gateway-queue-bytes 134217728 \
  -outage-scraper-memory-limit 8Gi \
  -outage-backend-memory-limit 1Gi \
  -outage-scrape-series 100000 \
  -outage-scrape-interval 30s \
  -outage-output "$OUTPUT_DIR"
```

### Read the results

<details>
<summary>Example queue-overhead result (100,000 series per scrape)</summary>

Summary from `task scale:outage -- -outage-mode queue-overhead
-outage-scrape-series 100000`: two outage scrapes with other settings at their
defaults. The artifact path is shortened.

```text
results: /tmp/queue-overhead-example/report.json
scraper (.resource_summary.scraper, .phase_metrics.*.scraper, .scraper_client_impact):
  peak heap 310.1 MiB; RSS 541.8 MiB; queue 22.1 MiB (8.6%)
  baseline: CPU 5.57s; RPCs 38, failed 0; mean 349.3ms, p95 ~486.8ms
  outage: CPU 5.34s; RPCs 38, failed 0; mean 268.4ms, p95 ~480.2ms
  recovery: CPU 0.50s; RPCs 0, failed 0
  refused 0 points
  backlog at backend restore: 0.0 MiB (.at_backend_restore_scraper_queue_bytes)
gateway (.resource_summary.gateway, .phase_metrics.*.gateway, .gateway_impact):
  peak heap 329.4 MiB; RSS 456.2 MiB; queue 65.6 MiB (51.3%)
  limiter: hard 80.0%; spike 25.0%; soft 55.0% (.gateway_memory_limiter)
  baseline: CPU 6.15s; RPCs 38, failed 0; mean 321.8ms, p95 ~486.0ms
  outage: CPU 5.94s; RPCs 38, failed 0; mean 250.9ms, p95 ~476.2ms
  recovery: CPU 0.55s; RPCs 0, failed 0
  refused 0 points; memory-limiter refused N/A; enqueue failed 0
delivery (.result): 2 scrapes; 200000 produced, 200000 received; estimated missing 0, duplicates 0; recovery 35.16s (.recovery_seconds)
delivery verification (.verification_complete): complete
combined sampled peak (.resource_summary.combined): heap 627.6 MiB; RSS 981.8 MiB
queue overhead after GC (.queue_only_overhead_estimate): 3.38x heap/serialized bytes; 344.1 serialized and 1163.7 additional heap bytes/point
--- PASS: TestBackendOutage (184.42s)
PASS
```

</details>

The estimate requires all produced points at the gateway, an empty scraper
queue, no active gateway RPC handlers, and no refusals or export failures.
If these checks fail, it is marked inconclusive and recovery still runs.
`.queue_only_overhead_estimate` compares baseline and outage after-GC heap
profiles with serialized queue bytes and point count. This removes pending
incoming requests from the measurement, but still includes changes in export
buffers and other retained allocations. Repeat with different scrape counts
to check how heap grows with queue occupancy.
When `valid` is true, `serialized_bytes_per_point` and
`heap_to_serialized_bytes_ratio` provide the measured payload size and heap
multiplier: the after-GC heap increase divided by serialized queue bytes.

Measurements with a 1 GiB gateway, a 128 MiB queue, and two 100,000-point scrapes
produced the following approximate ratios:

| Scrape template | Text bytes/series¹ | Labels/series | Additional heap / serialized bytes |
| --- | ---: | ---: | ---: |
| KSM | 192 | 5 | 2.2–2.6× |
| DCGM | 384 | 10 | 2.1–2.3× |
| Mixed | 192 | 10 | 3.4–3.7× |

¹ Text bytes per sample; the ratio uses serialized OTLP bytes.
Measurements include the test's two
[verification attributes](scraper.yaml), `run_id` and `sequence`.

More attributes relative to payload bytes can increase this ratio. The Collector
stores each attribute as a
[`KeyValue` structure](https://github.com/open-telemetry/opentelemetry-collector/blob/62cdad2ea133239380b44d20d84eb26e114779b6/pdata/internal/generated_proto_keyvalue.go#L18-L22)
and [grows the attribute array while decoding](https://github.com/open-telemetry/opentelemetry-collector/blob/62cdad2ea133239380b44d20d84eb26e114779b6/pdata/internal/generated_proto_numberdatapoint.go#L421-L435).
Those structures and spare array capacity consume heap even when label values
are short. Longer values increase serialized size without adding more attribute
entries. Mixed therefore combines many attribute entries with a small payload,
while DCGM spreads that structural cost across more payload bytes.

These ratios are estimates for these workloads, not universal sizing factors.
Attribute-array allocation boundaries, retained export buffers, and variation
in baseline heap affect them.
