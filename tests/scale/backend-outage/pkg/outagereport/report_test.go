// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package outagereport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decode(t *testing.T, data string) Report {
	t.Helper()
	r, err := Decode([]byte(data))
	require.NoError(t, err)
	return r
}

func TestSummaryMeasurements(t *testing.T) {
	r := decode(t, `{
		"mode":"outage",
		"verification_complete":true,
		"result":{"scrapes":2,"attempted_points":200000,"received_points":200000,"missing_points_estimate":0,"duplicate_points_estimate":0},
		"resource_summary":{"scraper":{"peak_heap_bytes":3145728}},
		"samples":[
			{"phase":"baseline","scraper.otelcol_process_runtime_heap_alloc_bytes":1048576},
			{"phase":"baseline_draining","scraper.otelcol_process_runtime_heap_alloc_bytes":2097152},
			{"phase":"outage","scraper.otelcol_process_runtime_heap_alloc_bytes":3145728,"scraper.otelcol_exporter_queue_size":1048576,"scraper.otelcol_exporter_queue_capacity":2097152}
		]
	}`)
	s := Summary("report.json", r)
	assert.Contains(t, s, "RSS N/A")
	assert.Contains(t, s, "baseline: CPU N/A; peak heap 2.0 MiB")
	assert.Contains(t, s, "outage: CPU N/A; peak heap 3.0 MiB; RSS N/A; queue 1.0 MiB (50.0%)")
	assert.Contains(t, s, "200000 produced, 200000 received")
	partial := Summary("partial.json", decode(t, `{"verification_complete":false,"verification_error":"delivery timed out"}`))
	assert.Contains(t, partial, "incomplete")
	assert.Contains(t, partial, "delivery timed out")
	assert.Contains(t, partial, "heap N/A")
	for _, test := range []struct{ name, latency, want, markdown string }{
		{"p95 without mean", `{"p95_seconds":0.2}`, "~200.0ms", "~200.0ms"},
		{"p95 bound without mean", `{"p95_lower_bound_seconds":0.2}`, ">=200.0ms", "&gt;=200.0ms"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := decode(t, `{"phase_metrics":{"outage":{"scraper":{"rpc_latency":`+test.latency+`}}}}`)
			assert.Contains(t, Summary("report.json", r), "; mean N/A, p95 "+test.want)
			comparison := Compare([2]string{"a.json", "b.json"}, [2]Report{r, {}})
			assert.Contains(t, comparison, "| outage RPC mean (completed calls) | N/A | N/A |")
			assert.Contains(t, comparison, "| outage RPC p95 (completed calls, estimated) | "+test.markdown+" | N/A |")
		})
	}
}

func TestCompareModesAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, left, right string
		contains, absent  []string
	}{
		{
			"test status and missing measurements",
			`{"test_status":"PASS","verification_complete":true,"gateway_impact":{"memory_limiter_refused_metric_points":0}}`,
			`{"test_status":"FAIL","verification_complete":true,"verification_error":"delivery timed out"}`,
			[]string{"| Test result | PASS | FAIL |", "delivery timed out", "| Memory-limiter refused points (attempts) | 0 | N/A |"},
			[]string{"Queue overhead after GC", "Requested overhead scrapes"},
		},
		{
			"valid and inconclusive overhead",
			`{"mode":"queue-overhead","queue_only_overhead_estimate":{"valid":true,"heap_to_serialized_bytes_ratio":3.66,"serialized_bytes_per_point":344.1}}`,
			`{"mode":"queue-overhead","queue_only_overhead_estimate":{"valid":false,"reason":"pending RPCs","heap_to_serialized_bytes_ratio":99}}`,
			[]string{"| Estimate | valid | inconclusive |", "| Estimate reason | N/A | pending RPCs |", "| Additional heap / serialized bytes | 3.66x | N/A |", "| Serialized bytes/point | 344.1 | N/A |"},
			[]string{"Outage input duration", "99.00x"},
		},
		{
			"CPU excludes baseline and preserves missing data",
			`{"phase_metrics":{"baseline":{"scraper":{"cpu_seconds":100}},"outage":{"scraper":{"cpu_seconds":1},"gateway":{"cpu_seconds":2}},"recovery":{"scraper":{"cpu_seconds":3},"gateway":{"cpu_seconds":4}}}}`,
			`{"phase_metrics":{"outage":{"scraper":{"cpu_seconds":0}},"recovery":{"scraper":{"cpu_seconds":0}}}}`,
			[]string{"| CPU, outage + recovery | 4.00 CPU-seconds | 0.00 CPU-seconds |", "| CPU, outage + recovery | 6.00 CPU-seconds | N/A |", "| Combined CPU, outage + recovery | 10.00 CPU-seconds | N/A |"}, nil,
		},
		{
			"small scrape sizes remain distinguishable",
			`{"scrape_bytes":10240}`, `{"scrape_bytes":20480}`,
			[]string{"| Scrape text size | 10.0 KiB | 20.0 KiB |"}, nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reports := [2]Report{decode(t, test.left), decode(t, test.right)}
			s := Compare([2]string{"a.json", "b.json"}, reports)
			for _, want := range test.contains {
				assert.Contains(t, s, want)
			}
			for _, unwanted := range test.absent {
				assert.NotContains(t, s, unwanted)
			}
		})
	}
	invalid := decode(t, `{"mode":"queue-overhead","queue_only_overhead_estimate":{"valid":false,"reason":"pending RPCs","heap_to_serialized_bytes_ratio":99}}`)
	s := Summary("report.json", invalid)
	assert.Contains(t, s, "inconclusive (pending RPCs)")
	assert.NotContains(t, s, "99.00x")
}
