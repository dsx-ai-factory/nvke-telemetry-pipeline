# Busy Node Scenario

This document describes a typical busy node, for sizing and testing the NKTP node Collector DaemonSet.
It describes not only telemetry volumes, but also specific telemetry sources which themselves may influence the DaemonSet's total workload.

## Per-node load

| Signal | Ingestion path | Load |
| --- | --- | --- |
| Logs | Container stdout, read by the `file_log` receiver | ~1 GiB/hour |
| Metrics | 90% scraped through the per-node Target Allocator, 10% OTLP | 50,000 series at 30s |
| Traces | OTLP to the node Collector | ~1 GiB/hour |

## Per-signal breakdown

### Logs

| Size class | Rate | Volume |
| --- | ---: | ---: |
| 100 B | 360/s | about 124 MiB/hour |
| 64 KiB | 4/s | 900 MiB/hour |
| **Total** | | **~1 GiB/hour** |

- Logs are sized in bytes, not records. Bytes are more representative of potential bottlenecks and typically more relevant to users.
- Logs come from files. Container stdout is the dominant log path on a real node. The `file_log` receiver requires CPU and memory that generally exceed those of OTLP forwarding.
  This is especially true when tracking large numbers of files and performing detailed log processing, as is typically done with Pod logs.
- Large logs span several runtime lines. The runtime splits lines at 16 KiB, so `file_log` recombines each 64 KiB record from four partial lines.

### Metrics

| Source | Path | Sources | Series each | Series |
| --- | --- | ---: | ---: | ---: |
| Large node-local targets | Scraped | 4 | 10,000 | 40,000 |
| Small, scraped | Scraped | 50 | 100 | 5,000 |
| Small, instrumented | OTLP | 40 | 125 | 5,000 |
| **Total** | | **94** | | **50,000** |

- Metrics use the chart's default 30s scrape interval. OTLP metrics match it for consistency.
- Large node-local targets are dedicated Pods that simulate node components: the kubelet, cAdvisor, and the node-local KSM DaemonSet Pod. One similar sized endpoint is assumed.
- Large node-local targets dominate scraped series, as on a real node.
- Large node-local targets are not cluster-level large targets. Targets labeled as large targets, such as a central KSM Pod, may run on the node.
  They are scraped by the `large_target_metrics` pool, so they are not simulated here.

### Traces

| Size class | Rate | Volume |
| --- | ---: | ---: |
| 1 KiB | 160/s | about 563 MiB/hour |
| 64 KiB | 2/s | 450 MiB/hour |
| **Total** | | **~1 GiB/hour** |

- Traces are sized in bytes, not spans or traces. Bytes are more representative of potential bottlenecks and typically more relevant to users.
- Typical spans are larger than typical logs. Each span carries IDs, timestamps, a name, a kind, a status, and attributes such as the HTTP route.
- Large spans carry exception stack traces, database statements, or captured payloads. They use the same size as large logs, so the two signals are comparable.

## Pod groups

Each node runs 100 Pods. The following Pod groups are proposed for simplicity of test case definition.

| Group | Pods | Per Pod |
| --- | ---: | --- |
| Small, scraped | 50 | 100 series scraped, 100 B logs at 4/s |
| Small, instrumented | 40 | 125 series over OTLP, 100 B logs at 4/s, 1 KiB spans at 4/s |
| Large node-local targets | 4 | 10,000 series scraped |
| Large logs | 4 | 64 KiB logs at 1/s |
| Large traces | 2 | 64 KiB spans at 1/s |

- The small Pods exercise parallel ingestion from multiple files, scrape endpoints, and OTLP clients.
- Large telemetry sources run in dedicated Pods for simplicity.
- About half the small Pods represent traditional workloads, meaning they are not instrumented and present a Prometheus-style metrics endpoint for scraping.
- The remainder of small Pods represent instrumented workloads, meaning they push metrics and traces via OTLP.
- Every load node runs exactly this mix, and all of its telemetry is handled by that node's Collector.

## Design load

The above is meant to describe a realistic busy node.
However, in order to determine appropriate limits for such a node, it is proposed to scale the above numbers as follows.

| Signal | Factor | Busy node | Design load |
| --- | ---: | ---: | ---: |
| Logs | 4× | ~1 GiB/hour | ~4 GiB/hour |
| Metrics | 2× | 50,000 series | 100,000 series |
| Traces | 4× | ~1 GiB/hour | ~4 GiB/hour |

- Logs and traces get a larger factor because their volume varies more, for example during incidents or with debug logging.
- Every metric source doubles, to 20,000 series per large node-local target, 200 per small scraped target, and 250 per instrumented Pod.
