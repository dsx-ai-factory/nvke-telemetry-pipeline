// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package outagereport reads and presents backend-outage test reports.
package outagereport

import (
	"encoding/json"
	"fmt"
	"os"
)

// Report contains the persisted observations used by summaries and comparisons.
// Pointers distinguish missing measurements from measured zero values.
type Report struct {
	TestStatus                 string                      `json:"test_status"`
	Mode                       string                      `json:"mode"`
	ScrapeTemplate             string                      `json:"scrape_template"`
	ScrapeSeries               *float64                    `json:"scrape_series"`
	ScrapeBytes                *float64                    `json:"scrape_bytes"`
	ScrapeInterval             string                      `json:"scrape_interval"`
	OutageDuration             string                      `json:"outage_duration"`
	BackendDownSeconds         *float64                    `json:"backend_down_seconds"`
	MemoryLimitBytes           *float64                    `json:"memory_limit_bytes"`
	QueueCapacityBytes         *float64                    `json:"queue_capacity_bytes"`
	ScraperMemoryLimitBytes    *float64                    `json:"scraper_memory_limit_bytes"`
	ScraperQueueCapacityBytes  *float64                    `json:"scraper_queue_capacity_bytes"`
	ScraperBatchDisabled       *bool                       `json:"scraper_batch_processor_disabled"`
	BackendMemoryLimitBytes    *float64                    `json:"backend_memory_limit_bytes"`
	QueueOverheadScrapes       *float64                    `json:"queue_overhead_scrapes_requested"`
	QueueOverheadSettleTimeout string                      `json:"queue_overhead_settle_timeout"`
	GatewayMemoryLimiter       Metrics                     `json:"gateway_memory_limiter"`
	ResourceSummary            map[string]Resources        `json:"resource_summary"`
	PhaseMetrics               map[string]map[string]Phase `json:"phase_metrics"`
	ScraperClientImpact        Metrics                     `json:"scraper_client_impact"`
	GatewayImpact              Metrics                     `json:"gateway_impact"`
	ScraperQueueAtRestoreBytes *float64                    `json:"at_backend_restore_scraper_queue_bytes"`
	Result                     *Delivery                   `json:"result"`
	RecoverySeconds            *float64                    `json:"recovery_seconds"`
	RecoveryInput              *bool                       `json:"recovery_input"`
	RecoveryInputSeconds       *float64                    `json:"recovery_input_seconds"`
	RecoveryDrainSeconds       *float64                    `json:"recovery_drain_seconds"`
	VerificationComplete       *bool                       `json:"verification_complete"`
	VerificationError          string                      `json:"verification_error"`
	QueueOnlyOverheadEstimate  *QueueOverhead              `json:"queue_only_overhead_estimate"`
	GatewayVolume              *VolumeReport               `json:"gateway_volume"`
}

// Metrics preserves presence information for counters and limiter settings.
type Metrics map[string]float64

func (m Metrics) get(name string) *float64 {
	v, ok := m[name]
	if !ok {
		return nil
	}
	return &v
}

// Resources contains sampled peaks, including paired peaks for combined usage.
type Resources struct {
	Heap         *float64 `json:"peak_heap_bytes"`
	RSS          *float64 `json:"peak_rss_bytes"`
	QueueBytes   *float64 `json:"peak_queue_bytes"`
	QueuePercent *float64 `json:"peak_queue_percent"`
}

// Phase contains CPU and completed RPC observations for one Collector phase.
type Phase struct {
	CPUSeconds *float64 `json:"cpu_seconds"`
	RPCCalls   *float64 `json:"rpc_calls"`
	FailedRPCs *float64 `json:"failed_rpc_calls"`
	Latency    Latency  `json:"rpc_latency"`
}

// Latency contains histogram-derived latency estimates in seconds.
type Latency struct {
	Mean          *float64 `json:"mean_seconds"`
	P95           *float64 `json:"p95_seconds"`
	P95LowerBound *float64 `json:"p95_lower_bound_seconds"`
}

// Delivery contains point-count estimates, not point-level deduplication results.
type Delivery struct {
	Scrapes    *float64 `json:"scrapes"`
	Produced   *float64 `json:"attempted_points"`
	Received   *float64 `json:"received_points"`
	Missing    *float64 `json:"missing_points_estimate"`
	Duplicates *float64 `json:"duplicate_points_estimate"`
}

// QueueOverhead contains the baseline-adjusted live heap estimate after
// requests finish and GC runs.
type QueueOverhead struct {
	Valid                   *bool    `json:"valid"`
	Reason                  string   `json:"reason"`
	BaselineHeap            *float64 `json:"baseline_sampled_heap_bytes"`
	OutageHeap              *float64 `json:"sampled_heap_bytes"`
	AdditionalHeap          *float64 `json:"additional_sampled_heap_bytes"`
	SerializedBytesPerPoint *float64 `json:"serialized_bytes_per_point"`
	HeapRatio               *float64 `json:"heap_to_serialized_bytes_ratio"`
	HeapBytesPerPoint       *float64 `json:"additional_heap_bytes_per_point"`
}

// Load reads a saved report.
func Load(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	r, err := Decode(data)
	if err != nil {
		return Report{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Decode reads the current report schema.
func Decode(data []byte) (Report, error) {
	var r Report
	err := json.Unmarshal(data, &r)
	return r, err
}
