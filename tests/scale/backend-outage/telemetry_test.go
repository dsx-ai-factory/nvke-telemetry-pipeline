// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (r *run) readCounts(endpoint, name string) (map[int]int, error) {
	families, err := scrapeWithTimeout(r.ctx, "http://"+endpoint, r.config.VerificationTimeout)
	if err != nil {
		return nil, err
	}
	return receiptCounts(families, name, r.id)
}

// readMetrics reads a Collector telemetry endpoint, failing the test on
// scrape errors.
func (r *run) readMetrics(endpoint string) map[string]*dto.MetricFamily {
	r.t.Helper()
	metrics, err := scrape(r.ctx, "http://"+endpoint)
	require.NoError(r.t, err)
	return metrics
}

func receiptCounts(families map[string]*dto.MetricFamily, name, run string) (map[int]int, error) {
	delivered := map[int]int{}
	family := families[name]
	if family == nil {
		return nil, fmt.Errorf("counter %s absent", name)
	}
	if family.GetType() != dto.MetricType_COUNTER {
		return nil, fmt.Errorf("verification receipt metric must be a counter")
	}
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["run_id"] == run {
			sequence, err := strconv.Atoi(labels["sequence"])
			value := metric.GetCounter().GetValue()
			if err != nil || sequence < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.Trunc(value) != value {
				return nil, fmt.Errorf("invalid verification counter sequence=%q value=%g", labels["sequence"], value)
			}
			delivered[sequence] += int(value)
		}
	}
	return delivered, nil
}

type rpcObservation struct {
	calls, failed, sum float64
	buckets            map[float64]float64
}

func rpcStats(families map[string]*dto.MetricFamily, side string) *rpcObservation {
	prefix := "rpc_" + side
	for _, name := range []string{prefix + "_duration_milliseconds", "otelcol_" + prefix + "_duration_milliseconds", prefix + "_call_duration", "otelcol_" + prefix + "_call_duration", prefix + "_call_duration_seconds"} {
		family := families[name]
		if family == nil || family.GetType() != dto.MetricType_HISTOGRAM {
			continue
		}
		factor := 1.0
		if strings.HasSuffix(name, "_milliseconds") {
			factor = .001
		}
		stats := &rpcObservation{buckets: map[float64]float64{}}
		for _, metric := range family.Metric {
			h := metric.GetHistogram()
			count := float64(h.GetSampleCount())
			stats.calls += count
			stats.sum += h.GetSampleSum() * factor
			for _, bucket := range h.Bucket {
				stats.buckets[bucket.GetUpperBound()*factor] += float64(bucket.GetCumulativeCount())
			}
			failed := false
			for _, label := range metric.Label {
				switch label.GetName() {
				case "rpc_grpc_status_code":
					failed = failed || label.GetValue() != "0"
				case "error_type":
					failed = failed || label.GetValue() != ""
				case "rpc_response_status_code":
					failed = failed || (label.GetValue() != "OK" && label.GetValue() != "0")
				}
			}
			if failed {
				stats.failed += count
			}
		}
		return stats
	}
	return nil
}

// rpcMeasurements uses histogram deltas for calls completed between snapshots,
// including calls started earlier. Histogram bounds make p95 an estimate.
func rpcMeasurements(before, after map[string]*dto.MetricFamily, side string) (map[string]float64, map[string]any) {
	start, end := rpcStats(before, side), rpcStats(after, side)
	if end == nil {
		return nil, map[string]any{"available": false}
	}
	// Histograms are created lazily: a fresh Collector has no series until
	// its first RPC, so an absent initial snapshot represents zero calls.
	if start == nil {
		start = &rpcObservation{}
	}
	count, sum := end.calls-start.calls, end.sum-start.sum
	bounds := make([]float64, 0, len(end.buckets))
	for bound, value := range start.buckets {
		end.buckets[bound] -= value
	}
	for bound, value := range end.buckets {
		if value < 0 {
			return nil, map[string]any{"available": false, "error": "RPC histogram bucket reset"}
		}
		bounds = append(bounds, bound)
	}
	if count < 0 || sum < 0 || end.failed < start.failed {
		return nil, map[string]any{"available": false, "error": "RPC histogram counter reset"}
	}
	counts := map[string]float64{"rpc_calls": count, "failed_rpc_calls": end.failed - start.failed}
	latency := map[string]any{"available": true, "completed_calls": count}
	if count == 0 {
		return counts, latency
	}
	latency["mean_seconds"] = sum / count
	sort.Float64s(bounds)
	rank, previousBound, previousCount := .95*count, 0.0, 0.0
	for _, bound := range bounds {
		cumulative := end.buckets[bound]
		if cumulative >= rank {
			if math.IsInf(bound, 1) {
				latency["p95_lower_bound_seconds"] = previousBound
			} else if cumulative > previousCount {
				latency["p95_seconds"] = previousBound + (bound-previousBound)*(rank-previousCount)/(cumulative-previousCount)
			}
			break
		}
		previousBound, previousCount = bound, cumulative
	}
	if latency["p95_seconds"] == nil && latency["p95_lower_bound_seconds"] == nil {
		latency["p95_lower_bound_seconds"] = previousBound
	}
	return counts, latency
}

func impact(before, after map[string]*dto.MetricFamily, side string) map[string]float64 {
	counts, _ := rpcMeasurements(before, after, side)
	result := map[string]float64{}
	for key, value := range counts {
		result[key] = value
	}
	for _, name := range []string{"otelcol_exporter_send_failed_metric_points", "otelcol_exporter_enqueue_failed_metric_points", "otelcol_receiver_refused_metric_points"} {
		result[name] = metricValue(after, name) - metricValue(before, name)
	}
	return result
}

func phaseMetrics(before, after map[string]*dto.MetricFamily, side string) map[string]any {
	counts, latency := rpcMeasurements(before, after, side)
	result := map[string]any{"cpu_seconds": metricValue(after, "otelcol_process_cpu_seconds") - metricValue(before, "otelcol_process_cpu_seconds"), "rpc_latency": latency}
	for key, value := range counts {
		result[key] = value
	}
	return result
}

// resourceSummary computes individual peaks and combined peaks from
// paired samples.
func resourceSummary(samples []map[string]any) map[string]any {
	summary := map[string]any{}
	for _, target := range []struct{ name, prefix string }{{"scraper", "scraper."}, {"gateway", ""}} {
		peaks := map[string]float64{}
		for _, sample := range samples {
			for _, field := range []struct{ name, metric string }{{"peak_heap_bytes", "otelcol_process_runtime_heap_alloc_bytes"}, {"peak_rss_bytes", "otelcol_process_memory_rss"}, {"peak_queue_bytes", "otelcol_exporter_queue_size"}} {
				if value, ok := sample[target.prefix+field.metric].(float64); ok {
					peaks[field.name] = math.Max(peaks[field.name], value)
				}
			}
			used, hasUsed := sample[target.prefix+"otelcol_exporter_queue_size"].(float64)
			capacity, hasCapacity := sample[target.prefix+"otelcol_exporter_queue_capacity"].(float64)
			if hasUsed && hasCapacity && capacity > 0 {
				peaks["peak_queue_percent"] = math.Max(peaks["peak_queue_percent"], 100*used/capacity)
			}
		}
		summary[target.name] = peaks
	}
	combined := map[string]float64{}
	for _, sample := range samples {
		for _, field := range []struct{ name, metric string }{{"peak_heap_bytes", "otelcol_process_runtime_heap_alloc_bytes"}, {"peak_rss_bytes", "otelcol_process_memory_rss"}} {
			gateway, gatewayOK := sample[field.metric].(float64)
			scraper, scraperOK := sample["scraper."+field.metric].(float64)
			if gatewayOK && scraperOK {
				combined[field.name] = math.Max(combined[field.name], gateway+scraper)
			}
		}
	}
	summary["combined"] = combined
	return summary
}

// memoryLimiterRefused finds memory-limiter refusal counters across
// supported telemetry names, distinguishing an absent measurement from zero
// refusals.
func memoryLimiterRefused(families map[string]*dto.MetricFamily) (float64, bool) {
	for _, name := range []string{"otelcol_processor_memory_limiter_refused_metric_points", "otelcol_processor_refused_metric_points"} {
		family := families[name]
		if family == nil {
			family = families[name+"_total"]
		}
		if family == nil {
			continue
		}
		var total float64
		found := false
		for _, metric := range family.Metric {
			matches := name == "otelcol_processor_memory_limiter_refused_metric_points"
			for _, label := range metric.Label {
				if label.GetName() == "processor" || label.GetName() == "otelcol_component_id" {
					matches = label.GetValue() == "memory_limiter" || strings.HasPrefix(label.GetValue(), "memory_limiter/")
				}
			}
			if matches && metric.Counter != nil {
				total += metric.Counter.GetValue()
				found = true
			}
		}
		if found {
			return total, true
		}
	}
	return 0, false
}

func metricValue(families map[string]*dto.MetricFamily, name string) float64 {
	family := families[name]
	if family == nil {
		family = families[name+"_total"]
	}
	var value float64
	for _, metric := range family.GetMetric() {
		if metric.Counter != nil {
			value += metric.GetCounter().GetValue()
		} else if metric.Histogram != nil {
			value += float64(metric.GetHistogram().GetSampleCount())
		} else {
			value += metric.GetGauge().GetValue()
		}
	}
	return value
}

// profileHeap reads inuse_space by sample type, independent of pprof's text
// formatting and which sample type is marked as the default.
func profileHeap(data []byte) (int64, error) {
	p, err := profile.ParseData(data)
	if err != nil {
		return 0, err
	}
	for i, kind := range p.SampleType {
		if kind.Type == "inuse_space" && kind.Unit == "bytes" {
			var total int64
			for _, sample := range p.Sample {
				total += sample.Value[i]
			}
			return total, nil
		}
	}
	return 0, fmt.Errorf("pprof inuse_space sample type missing")
}

func sampledHeap(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return profileHeap(data)
}

// queueOverheadReady returns an empty reason only when all input is
// retained at the gateway, upstream exports drained, and gateway RPC handlers
// finished.
func queueOverheadReady(beforeGateway, gateway, beforeScraper, scraper map[string]*dto.MetricFamily, points int, goroutines []byte) string {
	for _, target := range []struct{ before, after map[string]*dto.MetricFamily }{{beforeGateway, gateway}, {beforeScraper, scraper}} {
		for _, name := range []string{"otelcol_receiver_refused_metric_points", "otelcol_exporter_enqueue_failed_metric_points", "otelcol_exporter_send_failed_metric_points"} {
			if metricValue(target.after, name) != metricValue(target.before, name) {
				return "refusals or exporter failures occurred"
			}
		}
	}
	startRPC, endRPC := rpcStats(beforeScraper, "client"), rpcStats(scraper, "client")
	if endRPC != nil && endRPC.failed > 0 && (startRPC == nil || endRPC.failed > startRPC.failed) {
		return "scraper RPC failures occurred"
	}
	if metricValue(scraper, "otelcol_exporter_queue_size") != 0 {
		return "scraper exports still pending"
	}
	accepted := metricValue(gateway, "otelcol_receiver_accepted_metric_points") - metricValue(beforeGateway, "otelcol_receiver_accepted_metric_points")
	sent := metricValue(gateway, "otelcol_exporter_sent_metric_points") - metricValue(beforeGateway, "otelcol_exporter_sent_metric_points")
	if accepted != float64(points) || sent != 0 {
		return "outage points not all retained at gateway"
	}
	if strings.Contains(string(goroutines), "google.golang.org/grpc.(*Server).processUnaryRPC") {
		return "gateway RPC handlers still active"
	}
	if metricValue(gateway, "otelcol_exporter_queue_size") <= 0 {
		return "gateway queue empty"
	}
	return ""
}

// waitForQueueOverhead requires two consecutive ready observations and
// stops early if failures invalidate the queue-only measurement.
func waitForQueueOverhead(ctx context.Context, timeout time.Duration, check func(context.Context) (string, error)) (bool, string, error) {
	return waitForSettled(ctx, timeout, "request drain", check)
}

// waitForSettled bounds a readiness check and requires two consecutive ready
// observations. Failures that invalidate input retention stop the wait early.
func waitForSettled(ctx context.Context, timeout time.Duration, label string, check func(context.Context) (string, error)) (bool, string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	drainedChecks := 0
	reason := "waiting for in-flight requests to finish"
	for {
		current, err := check(waitCtx)
		if ctx.Err() != nil {
			return false, reason, ctx.Err()
		}
		if waitCtx.Err() != nil {
			return false, fmt.Sprintf("%s timeout after %s: %s", label, timeout, reason), nil
		}
		if err != nil {
			return false, reason, err
		}
		if current == "" {
			drainedChecks++
		} else {
			reason, drainedChecks = current, 0
		}
		if drainedChecks >= 2 {
			return true, "", nil
		}
		if current == "refusals or exporter failures occurred" || current == "scraper RPC failures occurred" {
			return false, current, nil
		}
		if err := sleep(waitCtx, time.Second); err != nil {
			if ctx.Err() != nil {
				return false, reason, ctx.Err()
			}
			return false, fmt.Sprintf("%s timeout after %s: %s", label, timeout, reason), nil
		}
	}
}

// queueOverheadEstimate compares baseline-adjusted live heap with
// queued serialized bytes and point count; callers must first verify requests
// drained.
func queueOverheadEstimate(baseline, heap int64, queue float64, points int) map[string]any {
	result := map[string]any{"valid": false, "baseline_sampled_heap_bytes": baseline, "sampled_heap_bytes": heap, "serialized_queue_bytes": queue, "queued_metric_points": points, "heap_measurement": "pprof inuse_space after GC; baseline adjusted, with in-flight requests drained"}
	if points <= 0 || queue <= 0 || heap <= baseline {
		result["reason"] = "insufficient positive retained heap or queued data"
		return result
	}
	additional := float64(heap - baseline)
	result["valid"] = true
	result["additional_sampled_heap_bytes"] = additional
	result["heap_to_serialized_bytes_ratio"] = additional / queue
	result["serialized_bytes_per_point"] = queue / float64(points)
	result["additional_heap_bytes_per_point"] = additional / float64(points)
	result["overhead_bytes_per_point"] = (additional - queue) / float64(points)
	return result
}

var telemetryClient = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

func scrape(ctx context.Context, endpoint string) (map[string]*dto.MetricFamily, error) {
	return scrapeWithTimeout(ctx, endpoint, 3*time.Second)
}

func scrapeWithTimeout(ctx context.Context, endpoint string, timeout time.Duration) (map[string]*dto.MetricFamily, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	response, err := telemetryClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics HTTP %d", response.StatusCode)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(response.Body)
}

// capture saves a pprof response, including query parameters such as
// gc=1.
func capture(ctx context.Context, endpoint, profile, destination string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/debug/pprof/"+profile, nil)
	if err != nil {
		return err
	}
	response, err := telemetryClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("pprof HTTP %d", response.StatusCode)
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// receiptSummary estimates missing and duplicate points from totals per
// scrape; offsetting loss and duplication within one scrape cannot be
// distinguished.
func receiptSummary(expected, received map[int]int) map[string]int {
	result := map[string]int{"attempted_points": 0, "received_points": 0, "delivered_points_estimate": 0, "duplicate_points_estimate": 0, "missing_points_estimate": 0, "scrapes": len(expected)}
	for id, points := range expected {
		count := received[id]
		result["attempted_points"] += points
		result["received_points"] += count
		result["delivered_points_estimate"] += min(points, count)
		result["duplicate_points_estimate"] += max(0, count-points)
		result["missing_points_estimate"] += max(0, points-count)
	}
	return result
}

// testMetrics parses telemetry examples shared by the measurement
// tests.
func testMetrics(t *testing.T, text string) map[string]*dto.MetricFamily {
	t.Helper()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	require.NoError(t, err)
	return families
}

// testValues constructs observations directly; exposition decoding is
// covered in TestTelemetry rather than repeated for calculations.
func testValues(values map[string]float64) map[string]*dto.MetricFamily {
	families := map[string]*dto.MetricFamily{}
	for name, value := range values {
		v := value
		if strings.Contains(name, "queue") || strings.Contains(name, "heap") {
			families[name] = &dto.MetricFamily{Type: dto.MetricType_GAUGE.Enum(), Metric: []*dto.Metric{{Gauge: &dto.Gauge{Value: &v}}}}
		} else {
			families[name] = &dto.MetricFamily{Type: dto.MetricType_COUNTER.Enum(), Metric: []*dto.Metric{{Counter: &dto.Counter{Value: &v}}}}
		}
	}
	return families
}

func TestWaitForQueueOverhead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ready, reason, err := waitForQueueOverhead(ctx, 20*time.Millisecond, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	require.NoError(t, err)
	assert.False(t, ready)
	assert.Contains(t, reason, "request drain timeout")
}

func TestQueueOverhead(t *testing.T) {
	t.Run("live heap and incremental overhead", func(t *testing.T) {
		for _, defaultType := range []string{"", "inuse_space"} {
			p := &profile.Profile{
				SampleType:        []*profile.ValueType{{Type: "alloc_space", Unit: "bytes"}, {Type: "inuse_space", Unit: "bytes"}},
				DefaultSampleType: defaultType,
				Sample:            []*profile.Sample{{Value: []int64{500, 20}}, {Value: []int64{900, 40}}},
			}
			var data bytes.Buffer
			require.NoError(t, p.Write(&data))
			heap, err := profileHeap(data.Bytes())
			require.NoError(t, err)
			assert.Equal(t, int64(60), heap)
		}
		estimate := queueOverheadEstimate(100, 500, 200, 10)
		for key, want := range map[string]any{"valid": true, "heap_to_serialized_bytes_ratio": 2.0, "serialized_bytes_per_point": 20.0, "additional_heap_bytes_per_point": 40.0} {
			assert.Equal(t, want, estimate[key], key)
		}
		assert.Equal(t, false, queueOverheadEstimate(500, 400, 200, 10)["valid"])
	})
	t.Run("request draining", func(t *testing.T) {
		metrics := func(accepted, queue, refused float64) map[string]*dto.MetricFamily {
			return testValues(map[string]float64{
				"otelcol_receiver_accepted_metric_points": accepted,
				"otelcol_exporter_sent_metric_points":     0,
				"otelcol_exporter_queue_size":             queue,
				"otelcol_receiver_refused_metric_points":  refused,
			})
		}
		before := metrics(0, 0, 0)
		for _, test := range []struct {
			name, goroutines string
			refused          float64
			valid            bool
		}{
			{"requests drained", "", 0, true},
			{"refusals", "", 1, false},
			{"active RPC", "google.golang.org/grpc.(*Server).processUnaryRPC", 0, false},
		} {
			t.Run(test.name, func(t *testing.T) {
				reason := queueOverheadReady(before, metrics(10, 200, test.refused), before, before, 10, []byte(test.goroutines))
				assert.Equal(t, test.valid, reason == "", reason)
			})
		}
	})

}

func TestTelemetry(t *testing.T) {
	for _, test := range []struct{ name, side, metric, label, ok, failed string }{
		{"native client", "client", "rpc_client_call_duration", "rpc_response_status_code", "OK", "UNAVAILABLE"},
		{"native server", "server", "rpc_server_call_duration", "rpc_response_status_code", "OK", "RESOURCE_EXHAUSTED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var text strings.Builder
			fmt.Fprintf(&text, "# TYPE %s histogram\n", test.metric)
			for status, count := range map[string]int{test.ok: 7, test.failed: 3} {
				fmt.Fprintf(&text, "%s_bucket{%s=%q,le=\"+Inf\"} %d\n%s_sum{%s=%q} 0\n%s_count{%s=%q} %d\n", test.metric, test.label, status, count, test.metric, test.label, status, test.metric, test.label, status, count)
			}
			families := testMetrics(t, text.String())
			stats := rpcStats(families, test.side)
			require.NotNil(t, stats)
			assert.Equal(t, float64(10), stats.calls)
			assert.Equal(t, float64(3), stats.failed)
			other := "server"
			if test.side == "server" {
				other = "client"
			}
			assert.Nil(t, rpcStats(families, other), "client and server measurements must not mix")
		})
	}
	// The generic processor counter must exclude refusals from other processors.
	families := testMetrics(t, `# TYPE otelcol_processor_refused_metric_points counter
otelcol_processor_refused_metric_points{processor="memory_limiter"} 100
otelcol_processor_refused_metric_points{processor="memory_limiter/custom"} 20
otelcol_processor_refused_metric_points{processor="redaction/test"} 50
`)
	count, available := memoryLimiterRefused(families)
	assert.True(t, available)
	assert.Equal(t, float64(120), count)

	assert.Nil(t, rpcStats(nil, "client"))
	_, available = memoryLimiterRefused(nil)
	assert.False(t, available, "missing counters must not be reported as zero")
}

func TestRPCLatency(t *testing.T) {
	snapshot := func(metric string, count uint64, sum, lowBound, highBound float64, lowCount uint64) map[string]*dto.MetricFamily {
		return map[string]*dto.MetricFamily{metric: {Type: dto.MetricType_HISTOGRAM.Enum(), Metric: []*dto.Metric{{Histogram: &dto.Histogram{
			SampleCount: &count, SampleSum: &sum,
			Bucket: []*dto.Bucket{{UpperBound: &lowBound, CumulativeCount: &lowCount}, {UpperBound: &highBound, CumulativeCount: &count}},
		}}}}}
	}
	before := snapshot("rpc_client_call_duration", 10, 10, 1, 2, 10)
	after := snapshot("rpc_client_call_duration", 20, 25, 1, 2, 15)
	_, got := rpcMeasurements(before, after, "client")
	_, initial := rpcMeasurements(nil, after, "client")
	assert.Equal(t, float64(20), initial["completed_calls"], "initial RPC series is created lazily")
	assert.Equal(t, float64(10), got["completed_calls"])
	assert.Equal(t, 1.5, got["mean_seconds"])
	assert.InDelta(t, 1.9, got["p95_seconds"], .00001)
	_, milliseconds := rpcMeasurements(snapshot("rpc_client_duration_milliseconds", 10, 10000, 1000, 2000, 10), snapshot("rpc_client_duration_milliseconds", 20, 25000, 1000, 2000, 15), "client")
	assert.Equal(t, got, milliseconds, "milliseconds must be converted to seconds")
	_, reset := rpcMeasurements(after, before, "client")
	assert.Equal(t, false, reset["available"], "counter reset")
	_, idle := rpcMeasurements(after, after, "client")
	assert.Equal(t, float64(0), idle["completed_calls"])
	assert.Nil(t, idle["mean_seconds"], "no latency without completed calls")
	_, missing := rpcMeasurements(nil, nil, "client")
	assert.Equal(t, false, missing["available"])
}

func TestResourceSummary(t *testing.T) {
	samples := []map[string]any{
		{"otelcol_process_runtime_heap_alloc_bytes": float64(100), "scraper.otelcol_process_runtime_heap_alloc_bytes": float64(10), "otelcol_process_memory_rss": float64(150), "scraper.otelcol_process_memory_rss": float64(20), "otelcol_exporter_queue_size": float64(20), "otelcol_exporter_queue_capacity": float64(40)},
		{"otelcol_process_runtime_heap_alloc_bytes": float64(20), "scraper.otelcol_process_runtime_heap_alloc_bytes": float64(90), "otelcol_process_memory_rss": float64(30), "scraper.otelcol_process_memory_rss": float64(140), "otelcol_exporter_queue_size": float64(30), "otelcol_exporter_queue_capacity": float64(40)},
		{"otelcol_process_runtime_heap_alloc_bytes": float64(105)},
	}
	got := resourceSummary(samples)
	combined := got["combined"].(map[string]float64)
	gateway := got["gateway"].(map[string]float64)
	assert.Equal(t, float64(110), combined["peak_heap_bytes"], "combine paired samples, not independent peaks")
	assert.Equal(t, float64(170), combined["peak_rss_bytes"])
	assert.Equal(t, float64(105), gateway["peak_heap_bytes"])
	assert.Equal(t, float64(75), gateway["peak_queue_percent"])
}

func TestReceiptAccounting(t *testing.T) {
	result := receiptSummary(map[int]int{1: 800, 2: 800}, map[int]int{1: 1600, 2: 400})
	for name, want := range map[string]int{"attempted_points": 1600, "received_points": 2000, "delivered_points_estimate": 1200, "duplicate_points_estimate": 800, "missing_points_estimate": 400} {
		assert.Equal(t, want, result[name], name)
	}
	families := testMetrics(t, "# TYPE outage_received_points_total counter\noutage_received_points_total{run_id=\"run\",sequence=\"123\"} 800\noutage_received_points_total{run_id=\"other\",sequence=\"124\"} 800\n")
	counts, err := receiptCounts(families, "outage_received_points_total", "run")
	require.NoError(t, err)
	assert.Equal(t, map[int]int{123: 800}, counts)
	_, err = receiptCounts(nil, "outage_received_points_total", "run")
	assert.Error(t, err)
}
