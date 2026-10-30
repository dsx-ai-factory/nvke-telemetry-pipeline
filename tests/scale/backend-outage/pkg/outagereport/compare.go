// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package outagereport

import (
	"fmt"
	"path/filepath"
	"strings"
)

func escape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ", "`", "&#96;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func collectorCPU(r Report, collector string) *float64 {
	var total float64
	for _, phase := range []string{"outage", "recovery"} {
		v := r.PhaseMetrics[phase][collector].CPUSeconds
		if v == nil {
			return nil
		}
		total += *v
	}
	return &total
}

func combinedCPU(r Report) *float64 {
	var total float64
	for _, collector := range []string{"scraper", "gateway"} {
		v := collectorCPU(r, collector)
		if v == nil {
			return nil
		}
		total += *v
	}
	return &total
}

// Compare formats two reports in pipeline order, with mode-specific overhead
// rows and the recorded overall test result. It does not infer missing results.
func Compare(paths [2]string, reports [2]Report) string {
	var out strings.Builder
	labels := [2]string{filepath.Base(filepath.Dir(paths[0])), filepath.Base(filepath.Dir(paths[1]))}
	if labels[0] == labels[1] || labels[0] == "." || labels[1] == "." {
		labels = [2]string{paths[0], paths[1]}
	}
	fmt.Fprintf(&out, "| Measurement | %s | %s |\n|---|---:|---:|\n", escape(labels[0]), escape(labels[1]))
	row := func(label string, get func(Report) string) {
		fmt.Fprintf(&out, "| %s | %s | %s |\n", label, escape(get(reports[0])), escape(get(reports[1])))
	}
	numeric := func(label string, u unit, get func(Report) *float64) {
		row(label, func(r Report) string { return number(get(r), u) })
	}
	section := func(label string) { fmt.Fprintf(&out, "| **%s** | | |\n", label) }
	hasOutage := reports[0].Mode == "outage" || reports[1].Mode == "outage"
	hasOverhead := reports[0].Mode == "queue-overhead" || reports[1].Mode == "queue-overhead"

	section("Configuration")
	row("Test result", testStatus)
	row("Mode", func(r Report) string { return text(r.Mode) })
	if reports[0].RecoveryInput != nil || reports[1].RecoveryInput != nil {
		row("Continued input during recovery", func(r Report) string { return boolean(r.RecoveryInput) })
		numeric("Recovery input duration", sec, func(r Report) *float64 { return r.RecoveryInputSeconds })
		numeric("Final recovery drain", sec, func(r Report) *float64 { return r.RecoveryDrainSeconds })
	}
	row("Scrape template", func(r Report) string { return text(r.ScrapeTemplate) })
	numeric("Series per scrape", count, func(r Report) *float64 { return r.ScrapeSeries })
	numeric("Scrape text size", size, func(r Report) *float64 { return r.ScrapeBytes })
	row("Scrape interval", func(r Report) string { return text(r.ScrapeInterval) })
	row("Scraper batch processor disabled", func(r Report) string { return boolean(r.ScraperBatchDisabled) })
	numeric("Backend memory limit", mib, func(r Report) *float64 { return r.BackendMemoryLimitBytes })
	if hasOutage {
		row("Outage input duration", func(r Report) string { return text(r.OutageDuration) })
	}
	if hasOverhead {
		numeric("Requested overhead scrapes", count, func(r Report) *float64 { return r.QueueOverheadScrapes })
		row("Request drain timeout", func(r Report) string { return text(r.QueueOverheadSettleTimeout) })
	}

	for _, target := range []struct {
		name, title      string
		memory, capacity func(Report) *float64
		impact           func(Report) Metrics
	}{
		{"scraper", "Scraper", func(r Report) *float64 { return r.ScraperMemoryLimitBytes }, func(r Report) *float64 { return r.ScraperQueueCapacityBytes }, func(r Report) Metrics { return r.ScraperClientImpact }},
		{"gateway", "Gateway", func(r Report) *float64 { return r.MemoryLimitBytes }, func(r Report) *float64 { return r.QueueCapacityBytes }, func(r Report) Metrics { return r.GatewayImpact }},
	} {
		section(target.title)
		numeric("Memory limit", mib, target.memory)
		numeric("Queue capacity", mib, target.capacity)
		numeric("Peak heap", mib, func(r Report) *float64 { return r.ResourceSummary[target.name].Heap })
		numeric("Peak RSS", mib, func(r Report) *float64 { return r.ResourceSummary[target.name].RSS })
		numeric("Peak queue bytes", mib, func(r Report) *float64 { return r.ResourceSummary[target.name].QueueBytes })
		numeric("Peak queue occupancy", pct, func(r Report) *float64 { return r.ResourceSummary[target.name].QueuePercent })
		numeric("CPU, outage + recovery", cpu, func(r Report) *float64 { return collectorCPU(r, target.name) })
		if target.name == "scraper" {
			for _, phase := range []string{"baseline", "outage", "recovery"} {
				numeric(phase+" RPC mean (completed calls)", ms, func(r Report) *float64 { return r.PhaseMetrics[phase][target.name].Latency.Mean })
				row(phase+" RPC p95 (completed calls, estimated)", func(r Report) string { return p95(r.PhaseMetrics[phase][target.name].Latency) })
			}
			numeric("Backlog at backend restoration", mib, func(r Report) *float64 { return r.ScraperQueueAtRestoreBytes })
		} else {
			numeric("Memory-limiter soft threshold", pct, func(r Report) *float64 { return r.GatewayMemoryLimiter.get("soft_limit_percentage") })
			numeric("Memory-limiter spike allowance", pct, func(r Report) *float64 { return r.GatewayMemoryLimiter.get("spike_limit_percentage") })
			numeric("Memory-limiter hard threshold", pct, func(r Report) *float64 { return r.GatewayMemoryLimiter.get("hard_limit_percentage") })
			numeric("Memory-limiter refused points (attempts)", count, func(r Report) *float64 { return target.impact(r).get("memory_limiter_refused_metric_points") })
		}
		numeric("Failed RPC attempts, outage + recovery", count, func(r Report) *float64 { return target.impact(r).get("failed_rpc_calls") })
		numeric("Receiver refused points (attempts)", count, func(r Report) *float64 { return target.impact(r).get("otelcol_receiver_refused_metric_points") })
		numeric("Enqueue-failed points (attempts)", count, func(r Report) *float64 { return target.impact(r).get("otelcol_exporter_enqueue_failed_metric_points") })
	}
	section("Combined resource cost")
	numeric("Combined peak heap (same sample)", mib, func(r Report) *float64 { return r.ResourceSummary["combined"].Heap })
	numeric("Combined peak RSS (same sample)", mib, func(r Report) *float64 { return r.ResourceSummary["combined"].RSS })
	numeric("Combined CPU, outage + recovery", cpu, combinedCPU)
	section("Delivery")
	if reports[0].VerificationError != "" || reports[1].VerificationError != "" {
		row("Verification error", func(r Report) string { return text(r.VerificationError) })
	}
	numeric("Successful post-baseline scrapes", count, func(r Report) *float64 {
		if r.Result == nil {
			return nil
		}
		return r.Result.Scrapes
	})
	numeric("Points produced", count, func(r Report) *float64 {
		if r.Result == nil {
			return nil
		}
		return r.Result.Produced
	})
	numeric("Points received", count, func(r Report) *float64 {
		if r.Result == nil {
			return nil
		}
		return r.Result.Received
	})
	numeric("Estimated missing points", count, func(r Report) *float64 {
		if r.Result == nil {
			return nil
		}
		return r.Result.Missing
	})
	numeric("Estimated duplicate points", count, func(r Report) *float64 {
		if r.Result == nil {
			return nil
		}
		return r.Result.Duplicates
	})
	numeric("Recovery time", sec, func(r Report) *float64 { return r.RecoverySeconds })
	numeric("Backend downtime (actual)", sec, func(r Report) *float64 { return r.BackendDownSeconds })

	if hasOverhead {
		section("Queue overhead after GC")
		row("Estimate", func(r Report) string { return overheadStatus(r.QueueOnlyOverheadEstimate) })
		if a, b := reports[0].QueueOnlyOverheadEstimate, reports[1].QueueOnlyOverheadEstimate; (a != nil && a.Reason != "") || (b != nil && b.Reason != "") {
			row("Estimate reason", func(r Report) string {
				if r.QueueOnlyOverheadEstimate == nil {
					return "N/A"
				}
				return text(r.QueueOnlyOverheadEstimate.Reason)
			})
		}
		for _, field := range []struct {
			label string
			unit  unit
			get   func(*QueueOverhead) *float64
		}{
			{"After-GC baseline heap", mib, func(q *QueueOverhead) *float64 { return q.BaselineHeap }},
			{"After-GC outage heap", mib, func(q *QueueOverhead) *float64 { return q.OutageHeap }},
			{"Baseline-adjusted additional heap", mib, func(q *QueueOverhead) *float64 { return q.AdditionalHeap }},
			{"Serialized bytes/point", bpp, func(q *QueueOverhead) *float64 { return q.SerializedBytesPerPoint }},
			{"Additional heap / serialized bytes", ratio, func(q *QueueOverhead) *float64 { return q.HeapRatio }},
			{"Additional heap bytes/point", bpp, func(q *QueueOverhead) *float64 { return q.HeapBytesPerPoint }},
		} {
			numeric(field.label, field.unit, func(r Report) *float64 {
				q := r.QueueOnlyOverheadEstimate
				if overheadStatus(q) != "valid" {
					return nil
				}
				return field.get(q)
			})
		}
	}

	fmt.Fprintf(&out, "\nSources: `%s` and `%s`.\n", escape(paths[0]), escape(paths[1]))
	return out.String()
}
