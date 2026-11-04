// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/backend-outage/pkg/outagereport"
	otelv1 "github.com/open-telemetry/opentelemetry-operator/apis/v1beta1"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
)

// run coordinates setup, baseline, backend outage, and recovery,
// assembling the report and optional queue-overhead measurements along the
// way.
type run struct {
	t                                  *testing.T
	ctx                                context.Context
	config                             runConfig
	id, output                         string
	cluster                            cluster
	gateway, scraper, backend          map[int]string
	report, phaseMetrics               map[string]any
	monitor                            *monitor
	volume                             *volumeMonitor
	baseline, expected                 map[int]int
	scrapeAttempts                     int
	baselineHeapPath                   string
	gatewayStart, clientStart          map[string]*dto.MetricFamily
	outageEndGateway, outageEndScraper map[string]*dto.MetricFamily
	backendDownAt                      time.Time
}

func newRun(t *testing.T, ctx context.Context, config runConfig) *run {
	t.Helper()
	r := &run{t: t, ctx: ctx, config: config, id: fmt.Sprintf("nktp-outage-%x", time.Now().UnixNano())}
	r.output = config.Output
	if r.output == "" {
		r.output = filepath.Join(os.TempDir(), r.id)
	}
	for _, name := range []string{"heaps", "logs", "diagnostics", "manifests", "prometheus-payload"} {
		require.NoError(t, os.MkdirAll(filepath.Join(r.output, name), 0755))
	}
	r.report = map[string]any{"run": r.id, "mode": config.Mode, "scrape_template": config.ScrapeTemplate, "scrape_series": config.ScrapeSeries}
	r.phaseMetrics = map[string]any{}
	// Register first so the saved status includes setup and cleanup failures.
	t.Cleanup(r.saveReport)
	t.Logf("mode=%s namespace=%s artifacts=%s", config.Mode, r.id, r.output)
	return r
}

func (r *run) queueOverhead() bool { return r.config.Mode == "queue-overhead" }

func (r *run) setup() {
	r.t.Helper()
	c, err := newCluster(r.config.Kubeconfig, r.output)
	require.NoError(r.t, err)
	r.cluster = c
	require.NoError(r.t, c.operatorPreflight(r.ctx, r.config.GatewayNamespace))
	var load corev1.PodList
	err = c.client.Resources("").List(r.ctx, &load, resources.WithLabelSelector("scale.nktp.io/run-id"))
	require.NoError(r.t, err)
	if len(load.Items) != 0 {
		r.t.Fatal("stop other scale generators before running the outage test")
	}
	gatewaySource, err := c.getCollector(r.ctx, r.config.GatewayNamespace, r.config.GatewaySource)
	require.NoError(r.t, err)
	gateway, err := deriveGateway(gatewaySource, r.config.Gateway)
	require.NoError(r.t, err)
	var claim *corev1.PersistentVolumeClaim
	if r.config.GatewayStorage != "" {
		claim, err = configureGatewayStorage(gateway, r.config.GatewayStorage, r.config.GatewayStorageClass)
		require.NoError(r.t, err)
	}
	limiter, err := configureGatewaySpike(gateway, r.config.GatewaySpike)
	require.NoError(r.t, err)
	backendSource, err := readCollector(r.config.BackendManifest)
	require.NoError(r.t, err)
	backend, err := deriveBackend(backendSource, r.config.BackendMemory)
	require.NoError(r.t, err)
	scraperSource, err := c.getCollector(r.ctx, r.config.ScraperNamespace, r.config.ScraperSource)
	require.NoError(r.t, err)
	instrumentation, err := readCollector("scraper.yaml")
	require.NoError(r.t, err)
	r.config.Scraper.RunID = r.id
	scraper, err := deriveScraper(scraperSource, instrumentation, r.config.Scraper)
	require.NoError(r.t, err)
	scrapePath := filepath.Join(r.output, "prometheus-payload", "metrics.prom")
	scrapeBytes, err := scrapePayload(r.config.ScrapeTemplate, scrapePath, r.config.ScrapeSeries)
	require.NoError(r.t, err)
	collectors := []*otelv1.OpenTelemetryCollector{backend, gateway, scraper}
	for i, name := range []string{"backend", "gateway", "scraper"} {
		data, err := json.MarshalIndent(collectors[i], "", "  ")
		require.NoError(r.t, err)
		require.NoError(r.t, os.WriteFile(filepath.Join(r.output, "manifests", name+"-opentelemetrycollector.json"), data, 0600))
	}
	err = c.client.Resources().Create(r.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.id}})
	require.NoError(r.t, err)
	r.cluster.namespace = r.id
	r.t.Cleanup(r.cleanup)
	if claim != nil {
		claim.Namespace = r.id
		require.NoError(r.t, r.cluster.client.Resources(r.id).Create(r.ctx, claim))
		data, err := json.MarshalIndent(claim, "", "  ")
		require.NoError(r.t, err)
		require.NoError(r.t, os.WriteFile(filepath.Join(r.output, "manifests", "gateway-pvc.json"), data, 0600))
	}
	require.NoError(r.t, r.cluster.installScrapeServer(r.ctx, scrapePath))
	for _, collector := range collectors {
		require.NoError(r.t, r.cluster.createCollector(r.ctx, collector))
	}
	for _, name := range []string{"outage-gateway", "outage-blackhole", "outage-scraper"} {
		r.cluster.ready(r.t, r.ctx, name)
	}
	r.gateway = r.cluster.forward(r.t, r.ctx, "outage-gateway", 8888, 1777)
	r.scraper = r.cluster.forward(r.t, r.ctx, "outage-scraper", 8888, 8889, 1777)
	r.backend = r.cluster.forward(r.t, r.ctx, "outage-blackhole", 8888, 8889, 1777)
	r.describeCollectors(gateway, scraper, backend, limiter, scrapeBytes)
	if claim != nil {
		r.volume, err = newVolumeMonitor(r.ctx, r.cluster, claim)
		require.NoError(r.t, err)
		r.volume.sample(r.ctx, "baseline")
	}
}

func (r *run) describeCollectors(gateway, scraper, backend *otelv1.OpenTelemetryCollector, limiter map[string]float64, scrapeBytes int64) {
	r.report["scrape_bytes"], r.report["scrape_interval"] = scrapeBytes, r.config.ScrapeInterval.String()
	r.report["verification_method"] = "received points per scrape timestamp; missing points and duplicates are estimates"
	if r.queueOverhead() {
		r.report["queue_overhead_settle_timeout"] = r.config.DrainTimeout.String()
	} else {
		r.report["outage_duration"] = r.config.Duration.String()
		r.report["recovery_input"] = r.config.RecoveryInput
	}
	r.report["gateway_memory_limiter"] = limiter
	r.report["gateway_memory_request_bytes"] = gateway.Spec.Resources.Requests.Memory().Value()
	for _, entry := range gateway.Spec.Env {
		if entry.Name == "GOMEMLIMIT" {
			r.report["gateway_gomemlimit"] = entry.Value
		}
	}
	for _, item := range []struct {
		collector                       *otelv1.OpenTelemetryCollector
		memoryKey, queueKey, percentKey string
	}{
		{gateway, "memory_limit_bytes", "queue_capacity_bytes", "queue_percent_of_memory_limit"},
		{scraper, "scraper_memory_limit_bytes", "scraper_queue_capacity_bytes", "scraper_queue_percent_of_memory_limit"},
	} {
		queue, err := sendingQueue(item.collector)
		require.NoError(r.t, err)
		memory := float64(item.collector.Spec.Resources.Limits.Memory().Value())
		r.report[item.memoryKey], r.report[item.queueKey] = memory, queue["queue_size"]
		if size, ok := queue["queue_size"].(float64); ok && queue["sizer"] == "bytes" {
			r.report[item.percentKey] = size / memory * 100
		}
	}
	queue, err := sendingQueue(gateway)
	require.NoError(r.t, err)
	if r.queueOverhead() && queue["sizer"] != "bytes" {
		r.t.Fatal("queue-overhead mode requires gateway queue sizer bytes")
	}
	r.report["backend_memory_limit_bytes"] = backend.Spec.Resources.Limits.Memory().Value()
	r.report["scraper_batch_processor_disabled"] = r.config.Scraper.DisableBatch
	r.report["phase_metrics"] = r.phaseMetrics
}

func (r *run) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, name := range []string{"outage-scraper", "outage-gateway", "outage-blackhole"} {
		logs, _ := r.cluster.logs(ctx, name+"-collector", "", 100, false)
		_ = os.WriteFile(filepath.Join(r.output, "logs", name+".log"), logs, 0600)
	}
	var pods corev1.PodList
	_ = r.cluster.client.Resources(r.id).List(ctx, &pods)
	data, _ := json.MarshalIndent(pods, "", "  ")
	_ = os.WriteFile(filepath.Join(r.output, "diagnostics", "pods.json"), data, 0600)
	if err := r.cluster.client.Resources().Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: r.id}}); err != nil {
		r.t.Log(err)
	}
}

// saveReport persists results after cleanup so test_status includes cleanup
// failures.
func (r *run) saveReport() {
	status := "PASS"
	if r.t.Failed() {
		status = "FAIL"
	} else if r.t.Skipped() {
		status = "SKIP"
	}
	r.report["test_status"] = status
	data, err := json.MarshalIndent(r.report, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(r.output, "report.json"), data, 0644)
	}
	if err != nil {
		r.t.Errorf("write report: %v", err)
	}
}

func (r *run) finishMonitoring() {
	r.monitor.stop(r.report)
	data, err := json.Marshal(r.report)
	if err != nil {
		r.t.Error(err)
		return
	}
	summary, err := outagereport.Decode(data)
	if err != nil {
		r.t.Errorf("format report: %v", err)
		return
	}
	r.t.Log(outagereport.Summary(filepath.Join(r.output, "report.json"), summary))
}

func (r *run) setPhase(phase string) { r.monitor.setPhase(phase); r.t.Log(phase) }

// pauseAndDrainScrapes hides the payload and allows an in-flight scrape to
// finish.
func (r *run) pauseAndDrainScrapes() {
	r.t.Helper()
	r.setScraping(false)
	require.NoError(r.t, sleep(r.ctx, min(r.config.ScrapeInterval, 10*time.Second)+time.Second))
}

// waitDelivery polls per-scrape receipt totals, optionally requiring both
// sending queues to drain before accepting delivery as complete. A nil expected
// map refreshes recovery input because backpressure can delay scrape receipts.
func (r *run) waitDelivery(expected map[int]int, duration time.Duration, drain bool) (map[int]int, error) {
	deadline := time.Now().Add(duration)
	last := map[int]int{}
	refreshInput := expected == nil
	for {
		var inputErr error
		if refreshInput {
			var updated map[int]int
			updated, inputErr = r.recoveryInput()
			if updated != nil {
				expected, r.expected = updated, updated
			}
		}
		counts, err := r.readCounts(r.backend[8889], "outage_received_points_total")
		if err == nil {
			last = counts
		}
		if err == nil {
			err = inputErr
		}
		result := receiptSummary(expected, last)
		if err == nil && refreshInput {
			accepted := int(metricValue(r.readMetrics(r.backend[8888]), "otelcol_receiver_accepted_metric_points"))
			if result["received_points"] != accepted {
				err = fmt.Errorf("receipt counts %d do not yet match backend accepted-point counter %d", result["received_points"], accepted)
			}
		}
		if err == nil && result["missing_points_estimate"] == 0 {
			if !drain || (metricValue(r.readMetrics(r.gateway[8888]), "otelcol_exporter_queue_size") == 0 && metricValue(r.readMetrics(r.scraper[8888]), "otelcol_exporter_queue_size") == 0) {
				return last, nil
			}
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("delivery counts=%v verification error=%v", result, err)
		}
		if err := sleep(r.ctx, time.Second); err != nil {
			return last, err
		}
	}
}

// recoveryInput finalizes produced counts after the backend can release blocked
// scrapes. Missing and partial receipts remain errors until all payload requests
// recorded at the final pause are accounted for or recovery times out.
func (r *run) recoveryInput() (map[int]int, error) {
	produced, err := r.readCounts(r.scraper[8889], "outage_scraped_points_total")
	if err != nil {
		return nil, err
	}
	expected := map[int]int{}
	for id, points := range produced {
		if _, existed := r.baseline[id]; !existed {
			expected[id] = points
		}
	}
	if len(expected) == 0 {
		return expected, fmt.Errorf("no successful scrapes after baseline")
	}
	if len(produced) != r.scrapeAttempts {
		return expected, fmt.Errorf("receipts account for %d of %d payload requests", len(produced), r.scrapeAttempts)
	}
	for id, points := range expected {
		if points != r.config.ScrapeSeries {
			return expected, fmt.Errorf("outage scrape %d has %d points, want %d", id, points, r.config.ScrapeSeries)
		}
	}
	return expected, nil
}

// runBaseline verifies complete delivery without refusals or exporter
// failures. Queue-overhead mode also captures an idle heap after forcing GC.
func (r *run) runBaseline() {
	r.t.Helper()
	startGateway, startScraper := r.readMetrics(r.gateway[8888]), r.readMetrics(r.scraper[8888])
	start := time.Now()
	r.setScraping(true)
	if err := sleep(r.ctx, r.config.Baseline); err != nil {
		r.t.Fatal(err)
	}
	endGateway, endScraper := r.readMetrics(r.gateway[8888]), r.readMetrics(r.scraper[8888])
	seconds := time.Since(start).Seconds()
	gatewayCPU := (metricValue(endGateway, "otelcol_process_cpu_seconds") - metricValue(startGateway, "otelcol_process_cpu_seconds")) / seconds
	scraperCPU := (metricValue(endScraper, "otelcol_process_cpu_seconds") - metricValue(startScraper, "otelcol_process_cpu_seconds")) / seconds
	r.setPhase("baseline_draining")
	r.pauseAndDrainScrapes()
	baselineReceived, err := r.waitDelivery(nil, r.config.VerificationTimeout, true)
	if err != nil {
		r.t.Fatalf("baseline not healthy: %v", err)
	}
	baseline := r.expected
	r.report["baseline_scrape_attempts"] = r.scrapeAttempts
	if receiptSummary(baseline, baselineReceived)["duplicate_points_estimate"] != 0 {
		r.t.Fatal("baseline duplicate receipts")
	}
	for _, endpoint := range []string{r.gateway[8888], r.scraper[8888]} {
		metrics := r.readMetrics(endpoint)
		for _, name := range []string{"otelcol_receiver_refused_metric_points", "otelcol_exporter_enqueue_failed_metric_points", "otelcol_exporter_send_failed_metric_points"} {
			if metricValue(metrics, name) != 0 {
				r.t.Fatalf("baseline %s nonzero; reduce scrape size", name)
			}
		}
	}
	if r.queueOverhead() {
		r.baselineHeapPath = filepath.Join(r.output, "heaps", "outage-gateway-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-baseline-after-gc.heap.pprof")
		require.NoError(r.t, capture(r.ctx, r.gateway[1777], "heap?gc=1", r.baselineHeapPath))
	}
	baselineMemory := r.readMetrics(r.gateway[8888])
	r.phaseMetrics["baseline"] = map[string]any{"gateway": phaseMetrics(startGateway, baselineMemory, "server"), "scraper": phaseMetrics(startScraper, r.readMetrics(r.scraper[8888]), "client")}
	r.report["baseline_scrapes"] = baseline
	r.t.Logf("baseline healthy: %d scrapes, gateway CPU %.2f, scraper CPU %.2f", len(baseline), gatewayCPU, scraperCPU)
	r.baseline = baseline
}

func (r *run) takeBackendDown() {
	r.t.Helper()
	r.setPhase("taking_backend_down")
	r.monitor.setBackend("")
	require.NoError(r.t, r.cluster.stopBackend(r.ctx), "backend did not scale down")
	r.gatewayStart, r.clientStart = r.readMetrics(r.gateway[8888]), r.readMetrics(r.scraper[8888])
	r.backendDownAt = time.Now()
	r.report["backend_down_at"] = r.backendDownAt
}

// runOutage collects input while the backend is down. Normal outage mode keeps
// scraping through restoration and recovery; other modes pause before recovery.
func (r *run) runOutage() {
	r.t.Helper()
	r.setPhase("outage")
	r.setScraping(true)
	if r.queueOverhead() {
		r.report["queue_overhead_scrapes_requested"] = r.config.OverheadScrapes
		// Bound missing/failed scrapes independently of the outage duration.
		inputDeadline := time.Now().Add(time.Duration(r.config.OverheadScrapes+1)*(r.config.ScrapeInterval) + r.config.VerificationTimeout)
		for time.Now().Before(inputDeadline) {
			counts, err := r.readCounts(r.scraper[8889], "outage_scraped_points_total")
			if err != nil {
				r.t.Fatal(err)
			}
			count := 0
			for id, points := range counts {
				if _, existed := r.baseline[id]; !existed && points == r.config.ScrapeSeries {
					count++
				}
			}
			if count >= r.config.OverheadScrapes {
				break
			}
			if err := sleep(r.ctx, 250*time.Millisecond); err != nil {
				r.t.Fatal(err)
			}
		}
	} else if err := sleep(r.ctx, r.config.Duration); err != nil {
		r.t.Fatal(err)
	}
	if r.queueOverhead() {
		r.setPhase("outage_draining_scrape")
		r.setScraping(false)
	} else if !r.config.RecoveryInput {
		r.setPhase("outage_draining_scrape")
		r.pauseAndDrainScrapes()
	}
	if !r.queueOverhead() {
		if r.config.RecoveryInput {
			_, err := r.waitForOutageScrape()
			require.NoError(r.t, err)
		}
		return // Finalize input after backpressure clears during recovery.
	}
	produced, err := r.readCounts(r.scraper[8889], "outage_scraped_points_total")
	require.NoError(r.t, err)
	expected, err := postBaselineScrapes(produced, r.baseline, r.config.ScrapeSeries, true)
	require.NoError(r.t, err)
	if len(expected) == 0 {
		r.t.Fatal("no successful scrapes during outage")
	}
	r.report["outage_scrapes"] = expected
	r.expected = expected
}

// waitForOutageScrape lets active input finish at least one outage scrape
// before restoring the backend. Other in-flight scrapes remain in the counts
// for final accounting after recovery.
func (r *run) waitForOutageScrape() (map[int]int, error) {
	var counts map[int]int
	err := wait.PollUntilContextTimeout(r.ctx, 250*time.Millisecond, r.config.VerificationTimeout, true, func(ctx context.Context) (bool, error) {
		families, err := scrapeWithTimeout(ctx, "http://"+r.scraper[8889], r.config.VerificationTimeout)
		if err != nil {
			var transportErr *url.Error
			if errors.As(err, &transportErr) && ctx.Err() == nil {
				return false, nil // Retry transient metrics-request failures within the polling deadline.
			}
			return false, err
		}
		counts, err = receiptCounts(families, "outage_scraped_points_total", r.id)
		if err != nil {
			return false, err
		}
		for id, points := range counts {
			if _, existed := r.baseline[id]; !existed && points == r.config.ScrapeSeries {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return counts, fmt.Errorf("waiting for a completed outage scrape: %w", err)
	}
	return counts, nil
}

// postBaselineScrapes retains every successful input scrape after baseline.
// Counts can be partial while input is active; final verification requires
// complete scrapes so recovery traffic cannot be silently excluded.
func postBaselineScrapes(produced, baseline map[int]int, series int, complete bool) (map[int]int, error) {
	expected := map[int]int{}
	for id, points := range produced {
		if _, existed := baseline[id]; existed {
			continue
		}
		if complete && points != series {
			return nil, fmt.Errorf("scrape %d has %d points, want %d", id, points, series)
		}
		expected[id] = points
	}
	return expected, nil
}

// queueOverheadState saves a goroutine profile and checks a single pair of
// telemetry snapshots, returning the corresponding serialized queue size.
func (r *run) queueOverheadState(ctx context.Context, phase string) (string, float64, error) {
	path := filepath.Join(r.output, "diagnostics", "outage-gateway-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+phase+".goroutines.txt")
	if err := capture(ctx, r.gateway[1777], "goroutine?debug=1", path); err != nil {
		return "", 0, err
	}
	goroutines, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	gateway, err := scrape(ctx, "http://"+r.gateway[8888])
	if err != nil {
		return "", 0, err
	}
	scraper, err := scrape(ctx, "http://"+r.scraper[8888])
	if err != nil {
		return "", 0, err
	}
	points := 0
	for _, count := range r.expected {
		points += count
	}
	return queueOverheadReady(r.gatewayStart, gateway, r.clientStart, scraper, points, goroutines), metricValue(gateway, "otelcol_exporter_queue_size"), nil
}

func (r *run) waitForRequests() (bool, string) {
	r.t.Helper()
	r.setPhase("outage_draining_requests")
	if len(r.expected) != r.config.OverheadScrapes {
		return false, "scrape count differs from requested count"
	}
	ready, reason, err := waitForQueueOverhead(r.ctx, r.config.DrainTimeout, func(ctx context.Context) (string, error) {
		reason, _, err := r.queueOverheadState(ctx, "queue-overhead")
		return reason, err
	})
	require.NoError(r.t, err)
	return ready, reason
}

// captureOutageProfiles saves gateway and scraper profiles and returns the
// after-GC gateway heap in queue-overhead mode, with a capture-success flag.
func (r *run) captureOutageProfiles() (string, bool) {
	r.t.Helper()
	for _, target := range []struct {
		name  string
		ports map[int]string
	}{{"gateway", r.gateway}, {"scraper", r.scraper}} {
		for _, entry := range []struct{ profile, directory, suffix string }{{"heap", "heaps", "heap.pprof"}, {"goroutine?debug=1", "diagnostics", "goroutines.txt"}} {
			file := "outage-" + target.name + "-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-outage-end." + entry.suffix
			if err := capture(r.ctx, target.ports[1777], entry.profile, filepath.Join(r.output, entry.directory, file)); err != nil {
				r.t.Error(err)
			}
		}
	}
	if !r.queueOverhead() {
		return "", true
	}
	outageHeapPath := filepath.Join(r.output, "heaps", "outage-gateway-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-outage-end-after-gc.heap.pprof")
	if err := capture(r.ctx, r.gateway[1777], "heap?gc=1", outageHeapPath); err != nil {
		r.t.Error(err)
		return outageHeapPath, false
	}
	return outageHeapPath, true
}

// estimateQueueOverhead rechecks that requests drained after GC and compares
// retained gateway heap with queued serialized bytes.
func (r *run) estimateQueueOverhead(queueOnlyReady bool, queueOnlyReason, outageHeapPath string) {
	r.t.Helper()
	estimate := map[string]any{"valid": false, "reason": queueOnlyReason}
	if queueOnlyReady {
		points := 0
		for _, count := range r.expected {
			points += count
		}
		finalReason, queueBytes, err := r.queueOverheadState(r.ctx, "queue-overhead-after-gc")
		require.NoError(r.t, err)
		baselineHeap, baselineErr := sampledHeap(r.baselineHeapPath)
		heap, heapErr := sampledHeap(outageHeapPath)
		if baselineErr != nil || heapErr != nil {
			r.t.Errorf("queue overhead profiles: baseline=%v outage=%v", baselineErr, heapErr)
			estimate["reason"] = "heap profile parsing failed"
		} else if finalReason != "" {
			estimate["reason"] = finalReason
		} else {
			estimate = queueOverheadEstimate(baselineHeap, heap, queueBytes, points)
		}
	}
	estimate["baseline_heap_file"], _ = filepath.Rel(r.output, r.baselineHeapPath)
	estimate["outage_heap_file"], _ = filepath.Rel(r.output, outageHeapPath)
	r.report["queue_only_overhead_estimate"] = estimate
}

func (r *run) measureOutage() {
	r.t.Helper()
	var ready bool
	var reason string
	if r.queueOverhead() {
		ready, reason = r.waitForRequests()
	}
	heapPath, captured := r.captureOutageProfiles()
	if !captured {
		ready, reason = false, "heap capture failed"
	}
	if r.queueOverhead() {
		r.estimateQueueOverhead(ready, reason, heapPath)
	}
	r.outageEndGateway, r.outageEndScraper = r.readMetrics(r.gateway[8888]), r.readMetrics(r.scraper[8888])
	r.report["at_backend_restore_scraper_queue_bytes"] = metricValue(r.outageEndScraper, "otelcol_exporter_queue_size")
	r.phaseMetrics["outage"] = map[string]any{"gateway": phaseMetrics(r.gatewayStart, r.outageEndGateway, "server"), "scraper": phaseMetrics(r.clientStart, r.outageEndScraper, "client")}
}

func (r *run) runRecovery() {
	r.t.Helper()
	r.report["backend_restore_requested_at"] = time.Now()
	r.setPhase("restoring_backend")
	require.NoError(r.t, r.cluster.scaleBackend(r.ctx, 1))
	r.cluster.ready(r.t, r.ctx, "outage-blackhole")
	r.report["backend_down_seconds"] = time.Since(r.backendDownAt).Seconds()
	backend := r.cluster.forward(r.t, r.ctx, "outage-blackhole", 8888, 8889, 1777)
	r.backend = backend
	r.monitor.setBackend(backend[1777])
	r.setPhase("recovery")
	start := time.Now()
	drainStart := start
	var before map[int]int
	if r.config.RecoveryInput {
		var err error
		before, err = r.readCounts(r.scraper[8889], "outage_scraped_points_total")
		require.NoError(r.t, err)
		require.NoError(r.t, sleep(r.ctx, r.config.Recovery))
		r.report["recovery_input_seconds"] = time.Since(start).Seconds()
		r.setPhase("recovery_draining")
		drainStart = time.Now()
		r.pauseAndDrainScrapes()
	}
	received, deliveryErr := r.waitDelivery(nil, r.config.VerificationTimeout, true)
	if r.config.RecoveryInput {
		r.report["recovery_drain_seconds"] = time.Since(drainStart).Seconds()
		outageScrapes, recoveryScrapes := map[int]int{}, map[int]int{}
		for id, points := range r.expected {
			if _, existed := before[id]; existed {
				outageScrapes[id] = points
			} else {
				recoveryScrapes[id] = points
			}
		}
		r.report["outage_scrapes"] = outageScrapes
		r.report["recovery_scrapes"] = recoveryScrapes
	} else {
		r.report["outage_scrapes"] = r.expected
	}
	r.report["post_baseline_scrape_attempts"] = r.scrapeAttempts - len(r.baseline)
	r.report["recovery_seconds"] = time.Since(start).Seconds()
	result := receiptSummary(r.expected, received)
	r.report["result"] = result
	r.report["received_points_by_scrape"] = received
	r.report["verification_complete"] = deliveryErr == nil
	if deliveryErr != nil {
		r.report["verification_error"] = deliveryErr.Error()
		r.t.Errorf("recovery: %v", deliveryErr)
	}
	backendMetrics := r.readMetrics(backend[8888])
	result["backend_received_points_including_duplicates"] = int(metricValue(backendMetrics, "otelcol_receiver_accepted_metric_points"))
	if deliveryErr == nil && result["backend_received_points_including_duplicates"] != result["received_points"] {
		r.t.Error("receipt counts disagree with backend accepted-point counter")
	}
	r.recordRecoveryImpact()
}

func (r *run) recordRecoveryImpact() {
	clientEnd, gatewayEnd := r.readMetrics(r.scraper[8888]), r.readMetrics(r.gateway[8888])
	clientImpact := impact(r.clientStart, clientEnd, "client")
	gatewayImpact := impact(r.gatewayStart, gatewayEnd, "server")
	r.report["scraper_client_impact"], r.report["gateway_impact"] = clientImpact, gatewayImpact
	startMemoryRefused, _ := memoryLimiterRefused(r.gatewayStart)
	endMemoryRefused, hasMemoryRefused := memoryLimiterRefused(gatewayEnd)
	if hasMemoryRefused {
		gatewayImpact["memory_limiter_refused_metric_points"] = endMemoryRefused - startMemoryRefused
	}
	r.phaseMetrics["recovery"] = map[string]any{
		"gateway": phaseMetrics(r.outageEndGateway, gatewayEnd, "server"),
		"scraper": phaseMetrics(r.outageEndScraper, clientEnd, "client"),
	}
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestCollectorReporting(t *testing.T) {
	for _, test := range []struct {
		name, sizer       string
		capacity, percent any
	}{
		{"byte queue", "bytes", float64(1 << 30), 12.5},
		{"non-float capacity", "bytes", int64(1 << 30), nil},
		{"request queue", "requests", float64(1 << 30), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := testManifest(t, `otlp: {protocols: {grpc: {}}}`)
			queue, err := sendingQueue(collector)
			require.NoError(t, err)
			queue["sizer"], queue["queue_size"] = test.sizer, test.capacity
			run := run{t: t, report: map[string]any{}}
			run.describeCollectors(collector, collector, collector, nil, 0)
			for _, prefix := range []string{"", "scraper_"} {
				assert.Equal(t, float64(8<<30), run.report[prefix+"memory_limit_bytes"])
				assert.Equal(t, test.capacity, run.report[prefix+"queue_capacity_bytes"])
				assert.Equal(t, test.percent, run.report[prefix+"queue_percent_of_memory_limit"])
			}
		})
	}
}

// TestRecoveryInputAccounting reproduces a partial receipt snapshot followed by
// completed and newly reported scrapes after backpressure clears. Empty queues
// and matching backend counters must not hide an entirely unreported attempt.
func TestRecoveryInputAccounting(t *testing.T) {
	inputReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/input/metrics":
			inputReads++
			_, err := fmt.Fprintln(w, "# TYPE outage_scraped_points_total counter")
			assert.NoError(t, err)
			for id := 0; id <= 4; id++ {
				points := 100
				if inputReads < 3 {
					if id > 2 && inputReads == 1 {
						continue
					}
					if id == 2 && inputReads == 2 {
						points = 98
					}
				}
				_, err := fmt.Fprintf(w, "outage_scraped_points_total{run_id=\"run\",sequence=\"%d\"} %d\n", id, points)
				assert.NoError(t, err)
			}
		case "/receipts/metrics":
			_, err := fmt.Fprintln(w, "# TYPE outage_received_points_total counter")
			assert.NoError(t, err)
			lastID := 4
			if inputReads == 1 {
				lastID = 2
			}
			for id := 1; id <= lastID; id++ {
				_, err := fmt.Fprintf(w, "outage_received_points_total{run_id=\"run\",sequence=\"%d\"} 100\n", id)
				assert.NoError(t, err)
			}
		case "/backend/metrics":
			accepted := 400
			if inputReads == 1 {
				accepted = 200
			}
			_, err := fmt.Fprintf(w, "# TYPE otelcol_receiver_accepted_metric_points counter\notelcol_receiver_accepted_metric_points %d\n", accepted)
			assert.NoError(t, err)
		default:
			_, err := fmt.Fprintln(w, "# TYPE otelcol_exporter_queue_size gauge\notelcol_exporter_queue_size 0")
			assert.NoError(t, err)
		}
	}))
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")
	r := run{t: t, ctx: t.Context(), id: "run", config: runConfig{ScrapeSeries: 100, VerificationTimeout: time.Second},
		report: map[string]any{}, baseline: map[int]int{0: 100}, scrapeAttempts: 5, expected: map[int]int{1: 100, 2: 98},
		scraper: map[int]string{8889: address + "/input", 8888: address + "/scraper"},
		gateway: map[int]string{8888: address + "/gateway"},
		backend: map[int]string{8889: address + "/receipts", 8888: address + "/backend"},
	}
	received, err := r.waitDelivery(nil, 4*time.Second, true)
	require.NoError(t, err)
	assert.Equal(t, map[int]int{1: 100, 2: 100, 3: 100, 4: 100}, r.expected)
	summary := receiptSummary(r.expected, received)
	assert.Equal(t, 400, summary["attempted_points"])
	assert.Equal(t, 400, summary["received_points"])
	assert.Zero(t, summary["missing_points_estimate"])
	assert.Zero(t, summary["duplicate_points_estimate"])
	// The queues and visible receipts are complete, but an additional payload
	// request must still prevent success until the bounded wait expires.
	r.scrapeAttempts = 6
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	r.ctx = ctx
	_, err = r.waitDelivery(nil, time.Second, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
