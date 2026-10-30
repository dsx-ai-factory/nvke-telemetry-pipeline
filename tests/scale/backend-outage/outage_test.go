// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// kubeconfigPath selects the shared scale cluster or an explicitly supplied
// file.
var kubeconfigPath = flag.String("kubeconfig", "../.kubeconfig", "kubeconfig file relative to tests/scale/backend-outage (default: ../.kubeconfig)")

var outageManifest = flag.String("outage-manifest", "collectors.yaml", "verification backend native manifest")
var outageBackendMemoryLimit = flag.String("outage-backend-memory-limit", "1Gi", "verification Collector memory limit (empty uses manifest)")
var outageScraperMemoryLimit = flag.String("outage-scraper-memory-limit", "8Gi", "scraping Collector memory limit (empty inherits source)")
var outageScrapeInterval = flag.Duration("outage-scrape-interval", 30*time.Second, "Prometheus scrape interval")
var outageScrapeTemplate = flag.String("outage-scrape-template", "metrics-ksm-mixed.prom", "Prometheus template path (relative to tests/scale/backend-outage unless absolute)")
var outageScrapeSeries = flag.Int("outage-scrape-series", 175000, "total series per scrape, expanded from the selected scrape template")
var outageVerificationScrapeTimeout = flag.Duration("outage-verification-scrape-timeout", 60*time.Second, "timeout for scraping receipt counters")
var outageMode = flag.String("outage-mode", "outage", "test mode: outage or queue-overhead")
var outageDuration = flag.Duration("outage-duration", time.Minute, "backend downtime in outage mode only")
var outageBaseline = flag.Duration("outage-baseline", time.Minute, "healthy baseline scraping duration")
var outageRecovery = flag.Duration("outage-recovery", 3*time.Minute, "maximum recovery observation window")
var outageHeapInterval = flag.Duration("outage-heap-interval", 30*time.Second, "periodic Collector heap interval (0 disables)")
var outageQueueOverheadScrapes = flag.Int("queue-overhead-scrapes", 2, "queue-overhead mode: successful input scrapes after baseline, then pause input")
var outageQueueOverheadSettleTimeout = flag.Duration("queue-overhead-settle-timeout", time.Minute, "queue-overhead mode: maximum wait for in-flight requests to finish after pausing input")
var outageOutput = flag.String("outage-output", "", "artifact directory (default /tmp/nktp-outage-RUN)")
var outageGatewaySource = flag.String("outage-gateway-source", "nktp-gateway-metrics", "installed Collector to derive the gateway from")
var outageGatewaySourceNamespace = flag.String("outage-gateway-source-namespace", "nktp", "source gateway namespace")
var outageGatewayQueueBytes = flag.Int64("outage-gateway-queue-bytes", 128<<20, "gateway byte-sized sending queue (0 inherits source ratio unless queue-percent is positive)")
var outageGatewayMemoryLimit = flag.String("outage-gateway-memory-limit", "1Gi", "gateway memory limit (empty inherits source)")
var outageGatewayQueuePercent = flag.Float64("outage-gateway-queue-percent", 0, "gateway queue percentage of memory limit (replaces default bytes; exclusive with explicit positive queue-bytes)")
var outageGatewaySpikePercent = flag.Int("outage-gateway-spike-percent", -1, "gateway memory-limiter spike percentage of container memory (-1 inherits source; positive and below hard limit)")
var outageGatewayStorage = flag.String("outage-gateway-storage", "", "gateway persistent queue PVC size, e.g. 1Gi (empty uses in-memory queue)")
var outageGatewayStorageClass = flag.String("outage-gateway-storage-class", "", "persistent queue StorageClass (empty uses cluster default)")

var outageScraperImage = flag.String("outage-scraper-image", "", "scraper image override (k8s distribution defaults to matching contrib image for test counters)")
var outageScraperSource = flag.String("outage-scraper-source", "nktp-large-target-metrics", "installed Collector to derive the scraper from")
var outageScraperSourceNamespace = flag.String("outage-scraper-source-namespace", "nktp", "source scraper namespace")
var outageScraperQueueBytes = flag.Int64("outage-scraper-queue-bytes", 0, "override scraper byte-sized queue (0 inherits source)")
var outageScraperQueuePercent = flag.Float64("outage-scraper-queue-percent", 0, "scraper queue percentage of memory limit (0 inherits ratio; exclusive with queue-bytes)")
var outageScraperDisableBatch = flag.Bool("outage-scraper-disable-batch", false, "disable scraper batch processors; preserve exporter sending-queue batching")

// runConfig holds the scenario settings resolved from command-line
// flags.
type runConfig struct {
	Kubeconfig, Output, Mode, ScrapeTemplate, BackendManifest        string
	GatewaySource, GatewayNamespace, ScraperSource, ScraperNamespace string
	Gateway                                                          collectorOptions
	GatewaySpike                                                     int
	GatewayStorage, GatewayStorageClass                              string
	Scraper                                                          scraperOptions
	BackendMemory                                                    string
	ScrapeSeries, OverheadScrapes                                    int
	ScrapeInterval, Baseline, Duration, Recovery                     time.Duration
	VerificationTimeout, DrainTimeout, HeapInterval                  time.Duration
}

func configFromFlags(t *testing.T) runConfig {
	t.Helper()
	if flag.NArg() != 0 {
		t.Fatalf("unexpected positional arguments %q; Go stops parsing flags at the first positional argument. Check for spaces after continuation backslashes", flag.Args())
	}
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	require.NoError(t, validateMode(*outageMode, *outageDuration, *outageScrapeInterval, *outageQueueOverheadScrapes, *outageQueueOverheadSettleTimeout, explicit))
	if *outageGatewayStorage == "" && *outageGatewayStorageClass != "" {
		t.Fatal("-outage-gateway-storage-class requires -outage-gateway-storage")
	}
	if *outageGatewayStorage != "" && *outageMode == "queue-overhead" {
		t.Fatal("persistent storage requires outage mode; queue-overhead mode estimates in-memory queue heap")
	}
	if *outageScrapeSeries <= 0 || *outageScrapeInterval <= 0 || *outageBaseline < *outageScrapeInterval || *outageRecovery <= 0 || *outageVerificationScrapeTimeout <= 0 || *outageHeapInterval < 0 {
		t.Fatal("series/interval/recovery must be positive; baseline must cover at least one scrape interval")
	}
	kubeconfig := *kubeconfigPath
	if len(filepath.SplitList(kubeconfig)) != 1 {
		t.Fatal("kubeconfig must name exactly one file")
	}
	kubeconfig, err := filepath.Abs(kubeconfig)
	require.NoError(t, err)
	queueBytes := *outageGatewayQueueBytes
	if *outageGatewayQueuePercent > 0 && !explicit["outage-gateway-queue-bytes"] {
		queueBytes = 0
	}
	return runConfig{
		Kubeconfig: kubeconfig, Output: *outageOutput, Mode: *outageMode,
		ScrapeTemplate: *outageScrapeTemplate, BackendManifest: *outageManifest,
		GatewaySource: *outageGatewaySource, GatewayNamespace: *outageGatewaySourceNamespace,
		ScraperSource: *outageScraperSource, ScraperNamespace: *outageScraperSourceNamespace,
		Gateway:        collectorOptions{MemoryLimit: *outageGatewayMemoryLimit, QueueBytes: queueBytes, QueuePercent: *outageGatewayQueuePercent},
		GatewaySpike:   *outageGatewaySpikePercent,
		GatewayStorage: *outageGatewayStorage, GatewayStorageClass: *outageGatewayStorageClass,
		Scraper: scraperOptions{
			collectorOptions: collectorOptions{MemoryLimit: *outageScraperMemoryLimit, QueueBytes: *outageScraperQueueBytes, QueuePercent: *outageScraperQueuePercent},
			Interval:         *outageScrapeInterval, Image: *outageScraperImage, DisableBatch: *outageScraperDisableBatch,
		},
		BackendMemory: *outageBackendMemoryLimit, ScrapeSeries: *outageScrapeSeries,
		ScrapeInterval: *outageScrapeInterval, Baseline: *outageBaseline, Duration: *outageDuration,
		Recovery: *outageRecovery, VerificationTimeout: *outageVerificationScrapeTimeout,
		OverheadScrapes: *outageQueueOverheadScrapes, DrainTimeout: *outageQueueOverheadSettleTimeout,
		HeapInterval: *outageHeapInterval,
	}
}

func validateMode(mode string, duration, interval time.Duration, scrapes int, settleTimeout time.Duration, explicit map[string]bool) error {
	switch mode {
	case "outage":
		if explicit["queue-overhead-scrapes"] || explicit["queue-overhead-settle-timeout"] {
			return fmt.Errorf("queue-overhead flags require -outage-mode queue-overhead")
		}
		if duration < interval {
			return fmt.Errorf("-outage-duration must cover at least one scrape interval")
		}
	case "queue-overhead":
		if explicit["outage-duration"] {
			return fmt.Errorf("-outage-duration applies only to -outage-mode outage")
		}
		if scrapes <= 0 || settleTimeout <= 0 {
			return fmt.Errorf("queue-overhead scrapes and request drain timeout must be positive")
		}
	default:
		return fmt.Errorf("-outage-mode must be outage or queue-overhead, got %q", mode)
	}
	return nil
}

// TestBackendOutage measures queue retention, memory, and client backpressure
// through a healthy baseline, an isolated backend outage, and delivery
// recovery. Optional gateway persistence requires a provisioning StorageClass,
// pods/exec access for directory accounting, and nodes/proxy access for cAdvisor
// metrics. Volume observations are scoped to the gateway container and queue
// directory; missing metrics are recorded separately from delivery assertions.
// Cleanup deletes the test namespace and PVC; PV deletion follows the
// StorageClass reclaim policy.
func TestBackendOutage(t *testing.T) {
	config := configFromFlags(t)
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := newRun(t, ctx, config)
	run.setup()
	run.monitor = startMonitor(ctx, run.output, run.gateway, run.scraper, run.backend, config.HeapInterval, run.volume)
	defer run.finishMonitoring()
	run.runBaseline()
	run.takeBackendDown()
	run.runOutage()
	run.measureOutage()
	run.runRecovery()
}
