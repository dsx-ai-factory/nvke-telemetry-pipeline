// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package outagereport

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

type unit string

const (
	count unit = "count"
	size  unit = "size"
	mib   unit = "MiB"
	pct   unit = "%"
	sec   unit = "s"
	cpu   unit = "CPU-seconds"
	ms    unit = "ms"
	ratio unit = "x"
	bpp   unit = "bytes/point"
)

func number(v *float64, u unit) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "N/A"
	}
	switch u {
	case size:
		if *v < 1<<10 {
			return fmt.Sprintf("%.0f bytes", *v)
		}
		if *v < 1<<20 {
			return fmt.Sprintf("%.1f KiB", *v/(1<<10))
		}
		return number(v, mib)
	case mib:
		return fmt.Sprintf("%.1f MiB", *v/(1<<20))
	case pct:
		return fmt.Sprintf("%.1f%%", *v)
	case sec:
		return fmt.Sprintf("%.2fs", *v)
	case cpu:
		return fmt.Sprintf("%.2f CPU-seconds", *v)
	case ms:
		return fmt.Sprintf("%.1fms", *v*1000)
	case ratio:
		return fmt.Sprintf("%.2fx", *v)
	case bpp:
		return fmt.Sprintf("%.1f", *v)
	default:
		return fmt.Sprintf("%.0f", *v)
	}
}

func text(s string) string {
	if s == "" {
		return "N/A"
	}
	return s
}

func boolean(b *bool) string {
	if b == nil {
		return "N/A"
	}
	return fmt.Sprint(*b)
}

func verification(r Report) string {
	if r.VerificationComplete == nil {
		return "not recorded"
	}
	if *r.VerificationComplete {
		return "complete"
	}
	return "incomplete"
}

func testStatus(r Report) string {
	if r.TestStatus == "" {
		return "not recorded"
	}
	return r.TestStatus
}

func p95(l Latency) string {
	if l.P95 != nil {
		return "~" + number(l.P95, ms)
	}
	if l.P95LowerBound != nil {
		return ">=" + number(l.P95LowerBound, ms)
	}
	return "N/A"
}

func overheadStatus(q *QueueOverhead) string {
	if q == nil || q.Valid == nil {
		return "unavailable"
	}
	if *q.Valid {
		return "valid"
	}
	return "inconclusive"
}

// Summary formats the live test output and can also present a saved report.
// Unavailable measurements remain distinct from measured zero values.
func Summary(path string, r Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "results: %s\n", path)
	for _, target := range []struct {
		name, impactKey string
		impact          Metrics
	}{{"scraper", "scraper_client_impact", r.ScraperClientImpact}, {"gateway", "gateway_impact", r.GatewayImpact}} {
		peaks := r.ResourceSummary[target.name]
		fmt.Fprintf(&out, "%s (.resource_summary.%s, .phase_metrics.*.%s, .%s):\n", target.name, target.name, target.name, target.impactKey)
		fmt.Fprintf(&out, "  peak heap %s; RSS %s; queue %s (%s)\n", number(peaks.Heap, mib), number(peaks.RSS, mib), number(peaks.QueueBytes, mib), number(peaks.QueuePercent, pct))
		if target.name == "gateway" && r.GatewayMemoryLimiter != nil {
			fmt.Fprintf(&out, "  limiter: hard %s; spike %s; soft %s (.gateway_memory_limiter)\n", number(r.GatewayMemoryLimiter.get("hard_limit_percentage"), pct), number(r.GatewayMemoryLimiter.get("spike_limit_percentage"), pct), number(r.GatewayMemoryLimiter.get("soft_limit_percentage"), pct))
		}
		for _, phase := range []string{"baseline", "outage", "recovery"} {
			m, ok := r.PhaseMetrics[phase][target.name]
			if !ok {
				continue
			}
			fmt.Fprintf(&out, "  %s: CPU %s; RPCs %s, failed %s", phase, number(m.CPUSeconds, sec), number(m.RPCCalls, count), number(m.FailedRPCs, count))
			if m.Latency.Mean != nil || m.Latency.P95 != nil || m.Latency.P95LowerBound != nil {
				fmt.Fprintf(&out, "; mean %s, p95 %s", number(m.Latency.Mean, ms), p95(m.Latency))
			}
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "  refused %s points", number(target.impact.get("otelcol_receiver_refused_metric_points"), count))
		if target.name == "gateway" {
			fmt.Fprintf(&out, "; memory-limiter refused %s; enqueue failed %s", number(target.impact.get("memory_limiter_refused_metric_points"), count), number(target.impact.get("otelcol_exporter_enqueue_failed_metric_points"), count))
		}
		for _, failure := range []struct{ label, metric string }{{"enqueue failed", "otelcol_exporter_enqueue_failed_metric_points"}, {"send failed", "otelcol_exporter_send_failed_metric_points"}} {
			if v := target.impact.get(failure.metric); v != nil && *v > 0 && (target.name != "gateway" || failure.label != "enqueue failed") {
				fmt.Fprintf(&out, "; %s %s", failure.label, number(v, count))
			}
		}
		out.WriteByte('\n')
		if target.name == "scraper" {
			fmt.Fprintf(&out, "  backlog at backend restore: %s (.at_backend_restore_scraper_queue_bytes)\n", number(r.ScraperQueueAtRestoreBytes, mib))
		}
	}
	if d := r.Result; d != nil {
		fmt.Fprintf(&out, "delivery (.result): %s scrapes; %s produced, %s received; estimated missing %s, duplicates %s; recovery %s (.recovery_seconds)\n", number(d.Scrapes, count), number(d.Produced, count), number(d.Received, count), number(d.Missing, count), number(d.Duplicates, count), number(r.RecoverySeconds, sec))
	}
	fmt.Fprintf(&out, "delivery verification (.verification_complete): %s\n", verification(r))
	if r.VerificationError != "" {
		fmt.Fprintf(&out, "verification error (.verification_error): %s\n", r.VerificationError)
	}
	combined := r.ResourceSummary["combined"]
	fmt.Fprintf(&out, "combined sampled peak (.resource_summary.combined): heap %s; RSS %s", number(combined.Heap, mib), number(combined.RSS, mib))
	if r.Mode == "queue-overhead" {
		q := r.QueueOnlyOverheadEstimate
		if overheadStatus(q) == "valid" {
			fmt.Fprintf(&out, "\nqueue overhead after GC (.queue_only_overhead_estimate): %s heap/serialized bytes; %s serialized and %s additional heap bytes/point", number(q.HeapRatio, ratio), number(q.SerializedBytesPerPoint, bpp), number(q.HeapBytesPerPoint, bpp))
		} else {
			fmt.Fprintf(&out, "\nqueue overhead after GC (.queue_only_overhead_estimate): %s", overheadStatus(q))
			if q != nil && q.Reason != "" {
				fmt.Fprintf(&out, " (%s)", q.Reason)
			}
		}
	}
	if d := r.GatewayVolume; d != nil {
		fmt.Fprintf(&out, "\ngateway volume (.gateway_volume): peak allocated %s; peak growth %s; peak apparent %s; usage errors %d; I/O errors %d; counter resets %d; I/O activity observed %t",
			number(d.PeakAllocatedBytes, mib), number(d.PeakGrowthBytes, mib), number(d.PeakApparentBytes, mib), d.UsageErrors, d.IOErrors, d.CounterResets, d.IOActivityObserved)
		phases := make([]string, 0, len(d.Phases))
		for phase := range d.Phases {
			phases = append(phases, phase)
		}
		slices.Sort(phases)
		for _, phase := range phases {
			devices := make([]string, 0, len(d.Phases[phase]))
			for device := range d.Phases[phase] {
				devices = append(devices, device)
			}
			slices.Sort(devices)
			for _, device := range devices {
				io := d.Phases[phase][device]
				fmt.Fprintf(&out, "\n  %s %s (%.1fs sampled):", phase, device, io.Seconds)
				for _, field := range []struct{ key, label string }{
					{"container_fs_reads_bytes_total", "read bytes/s"}, {"container_fs_writes_bytes_total", "write bytes/s"},
					{"container_fs_reads_total", "read IOPS"}, {"container_fs_writes_total", "write IOPS"},
				} {
					fmt.Fprintf(&out, " %s avg %.1f peak %.1f;", field.label, io.Average[field.key], io.Peak[field.key])
				}
			}
		}
	}
	return out.String()
}
