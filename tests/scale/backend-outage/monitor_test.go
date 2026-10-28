// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

// monitor collects phase-tagged metrics and periodic heap profiles in
// the background.
type monitor struct {
	mu                     sync.Mutex
	workers                sync.WaitGroup
	cancel                 context.CancelFunc
	phase, backendEndpoint string
	samples, captures      []map[string]any
	volume                 *volumeMonitor
}

// startMonitor samples metrics every two seconds and captures heaps at
// the requested interval; a zero interval disables periodic heap capture.
func startMonitor(ctx context.Context, output string, gateway, scraper, backend map[int]string, interval time.Duration, volume *volumeMonitor) *monitor {
	monitorCtx, cancel := context.WithCancel(ctx)
	m := &monitor{cancel: cancel, phase: "baseline", backendEndpoint: backend[1777], volume: volume}
	if volume != nil {
		m.workers.Go(func() {
			for sleep(monitorCtx, 5*time.Second) == nil {
				m.mu.Lock()
				phase := m.phase
				m.mu.Unlock()
				volume.sample(monitorCtx, phase)
			}
		})
	}
	names := []string{"otelcol_exporter_queue_size", "otelcol_exporter_queue_capacity", "otelcol_process_runtime_heap_alloc_bytes", "otelcol_process_memory_rss", "otelcol_process_cpu_seconds", "otelcol_receiver_refused_metric_points", "otelcol_exporter_enqueue_failed_metric_points", "otelcol_exporter_send_failed_metric_points", "otelcol_exporter_sent_metric_points"}
	m.workers.Go(func() {
		for {
			row := map[string]any{"time": time.Now()}
			m.mu.Lock()
			row["phase"] = m.phase
			m.mu.Unlock()
			for _, target := range []struct{ prefix, endpoint string }{{"", gateway[8888]}, {"scraper.", scraper[8888]}} {
				metrics, err := scrape(monitorCtx, "http://"+target.endpoint)
				if err != nil {
					row[target.prefix+"error"] = err.Error()
					continue
				}
				if refused, available := memoryLimiterRefused(metrics); available {
					row[target.prefix+"memory_limiter_refused_metric_points"] = refused
				}
				for _, name := range names {
					if metrics[name] != nil || metrics[name+"_total"] != nil {
						row[target.prefix+name] = metricValue(metrics, name)
					}
				}
			}
			m.mu.Lock()
			m.samples = append(m.samples, row)
			m.mu.Unlock()
			if sleep(monitorCtx, 2*time.Second) != nil {
				return
			}
		}
	})
	if interval > 0 {
		m.workers.Go(func() {
			for {
				m.mu.Lock()
				currentPhase, backendEndpoint := m.phase, m.backendEndpoint
				m.mu.Unlock()
				stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
				for _, target := range []struct{ name, endpoint string }{{"outage-gateway", gateway[1777]}, {"outage-scraper", scraper[1777]}, {"outage-blackhole", backendEndpoint}} {
					if target.endpoint == "" {
						continue
					}
					file := filepath.Join("heaps", target.name+"-"+stamp+"-"+currentPhase+".heap.pprof")
					row := map[string]any{"collector": target.name, "phase": currentPhase, "file": file, "time": time.Now()}
					if err := capture(monitorCtx, target.endpoint, "heap", filepath.Join(output, file)); err != nil {
						row["error"] = err.Error()
					}
					m.mu.Lock()
					m.captures = append(m.captures, row)
					m.mu.Unlock()
				}
				if sleep(monitorCtx, interval) != nil {
					return
				}
			}
		})
	}

	return m
}

func (m *monitor) setPhase(phase string) { m.mu.Lock(); defer m.mu.Unlock(); m.phase = phase }
func (m *monitor) setBackend(endpoint string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backendEndpoint = endpoint
}

// stop joins the sampling workers before adding observations and peaks to the
// report.
func (m *monitor) stop(report map[string]any) {
	m.cancel()
	m.workers.Wait()
	if m.volume != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.volume.sample(ctx, m.phase)
		report["gateway_volume"] = m.volume.report()
	}
	report["samples"], report["heap_captures"] = m.samples, m.captures
	report["resource_summary"] = resourceSummary(m.samples)
}
