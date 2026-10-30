// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
)

// formatterObservations assigns ordered evaluation times to result fixtures.
func formatterObservations(values ...model.Value) []measurements.QueryObservation {
	start := time.Date(2026, 9, 17, 15, 4, 0, 0, time.UTC)
	observations := make([]measurements.QueryObservation, len(values))
	for i, value := range values {
		observations[i] = measurements.QueryObservation{EvaluatedAt: start.Add(time.Duration(i) * 15 * time.Second), Value: value}
	}
	return observations
}

// scalarResult represents a numeric query result without series labels.
func scalarResult(value float64) model.Value {
	return &model.Scalar{Timestamp: 100000, Value: model.SampleValue(value)}
}

// checkReportText checks that the report contains the expected text fragments.
func checkReportText(t *testing.T, got string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		assert.Contains(t, got, fragment, "report lacks %q:\n%s", fragment, got)
	}
}

// TestFormatThroughput checks the actual six-value throughput example and gaps.
func TestFormatThroughput(t *testing.T) {
	values := []model.Value{model.Vector{}, model.Vector{}, model.Vector{}, model.Vector{}, model.Vector{}, model.Vector{}}
	for _, value := range []float64{170.67068201136686, 197.07561167686123, 211.25352089201482, 211.25352089201482, 211.24647922534623, 211.24647922534623} {
		values = append(values, scalarResult(value))
	}
	observations := formatterObservations(values...)
	start := observations[0].EvaluatedAt
	report := measurements.Report{
		"started_at": start, "finished_at": start.Add(3 * time.Minute), "interval": "15s",
		"queries": map[string]measurements.QueryReport{"throughput": {Expression: "sum(rate(points_total[2m]))", Unit: "points/s", Observations: observations}},
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	got := Format(report)
	checkReportText(t, got, "Window:   3m0s", "Polling interval: 15s", "throughput [points/s]", "Attempts: 12",
		"Values: 6  No data: 6  Errors: 0", "Min: 170.671  Mean: 202.124  Max: 211.254  Last: 211.246")
	var rows []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			rows = append(rows, strings.Join(strings.Fields(line), " "))
		}
	}
	want := "[170.671, 178.787) 1\n[178.787, 186.904) 0\n[186.904, 195.02) 0\n[195.02, 203.137) 1\n[203.137, 211.254] 4"
	if strings.Join(rows, "\n") != want {
		t.Errorf("histogram rows = %q, want %q", rows, want)
	}
	after, err := json.Marshal(report)
	if err != nil || string(before) != string(after) {
		t.Errorf("formatting changed the raw report, marshal error = %v", err)
	}
	if strings.ContainsAny(got, "▁▂▃▄▅▆▇█") || strings.Contains(got, "Timeline:") || strings.Contains(got, "    Time ") {
		t.Errorf("numeric report contains a bar or timeline:\n%s", got)
	}
	t.Log("\n" + got)
}

// TestFormatSeries keeps exporters separate even when they share an instance.
func TestFormatSeries(t *testing.T) {
	a := model.Metric{"instance": "one", "exporter": "a"}
	b := model.Metric{"instance": "one", "exporter": "b"}
	observations := formatterObservations(
		model.Vector{&model.Sample{Metric: a, Value: 1024}},
		model.Vector{&model.Sample{Metric: a, Value: 2048}, &model.Sample{Metric: b, Value: 4096}},
		model.Vector{&model.Sample{Metric: b, Value: 8192}},
	)
	got := Format(measurements.Report{"queries": map[string]measurements.QueryReport{"queue": {Unit: "bytes", Observations: observations}}})
	checkReportText(t, got, "queue [KiB]", `Series: {exporter="a", instance="one"}`, `Series: {exporter="b", instance="one"}`,
		"Min: 1  Mean: 1.5  Max: 2  Last: no data", "Min: 4  Mean: 6  Max: 8  Last: 8")
	if strings.Count(got, "Values: 2  No data: 1") != 2 {
		t.Errorf("series were pooled or missing observations were lost:\n%s", got)
	}
}

// TestFormatExceptionalValues preserves gaps, errors and non-finite values.
func TestFormatExceptionalValues(t *testing.T) {
	observations := formatterObservations(scalarResult(0), scalarResult(math.NaN()), scalarResult(math.Inf(1)), scalarResult(math.Inf(-1)), nil, model.Vector{})
	observations[4].Error = "backend unavailable"
	observations[0].Warnings = []string{"partial response"}
	got := Format(measurements.Report{"queries": map[string]measurements.QueryReport{"load": {Observations: observations}}})
	checkReportText(t, got, "Values: 1  No data: 1  Errors: 1", "NaN: 1  +Inf: 1  -Inf: 1  Unsupported: 0",
		"Min: 0  Mean: 0  Max: 0  Last: no data", `"backend unavailable"`, `"partial response"`)
}

// TestFormatEmptyAndUnsupported prevents absent data from appearing as zero.
func TestFormatEmptyAndUnsupported(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []model.Value
		want   string
	}{
		{name: "empty", values: []model.Value{model.Vector{}, model.Vector{}}, want: "Values: 0  No data: 2"},
		{name: "none", want: "Attempts: 0"},
		{name: "matrix", values: []model.Value{model.Matrix{}}, want: "matrix is retained in the raw report"},
		{name: "string", values: []model.Value{&model.String{Value: "text"}}, want: "string is retained in the raw report"},
		{name: "histogram", values: []model.Value{model.Vector{&model.Sample{Metric: model.Metric{}, Histogram: &model.SampleHistogram{}}}}, want: "Unsupported: 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Format(measurements.Report{"queries": map[string]measurements.QueryReport{"load": {Observations: formatterObservations(test.values...)}}})
			checkReportText(t, got, test.want, "No finite values.")
			if strings.Contains(got, "Mean:") || strings.Contains(got, "Range ") {
				t.Errorf("report invented a numeric summary:\n%s", got)
			}
		})
	}
}

// TestFormatNumericRanges covers constant, signed, tiny and extreme values.
func TestFormatNumericRanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []float64
		unit   string
		want   string
	}{
		{name: "constant", values: []float64{42, 42}, want: "Min: 42  Mean: 42  Max: 42  Last: 42"},
		{name: "negative", values: []float64{-4096, 0, 4096}, unit: "bytes/s", want: "Min: -4  Mean: 0  Max: 4  Last: 4"},
		{name: "small", values: []float64{1e-9, 2e-9}, want: "Mean: 1.5e-09"},
		{name: "wide", values: []float64{-math.MaxFloat64, math.MaxFloat64}, want: "Mean: 0"},
		{name: "narrow", values: []float64{1, math.Nextafter(1, 2)}, want: "Values: 2"},
		{name: "subnormal", values: []float64{math.SmallestNonzeroFloat64, 2 * math.SmallestNonzeroFloat64}, want: "Values: 2"},
		{name: "large narrow", values: []float64{math.Nextafter(math.MaxFloat64, 0), math.MaxFloat64}, want: "Values: 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var values []model.Value
			for _, value := range test.values {
				values = append(values, scalarResult(value))
			}
			got := Format(measurements.Report{"queries": map[string]measurements.QueryReport{"load": {Unit: test.unit, Observations: formatterObservations(values...)}}})
			checkReportText(t, got, test.want, "Range ")
			_, table, _ := strings.Cut(got, "    Range")
			total := 0
			for _, line := range strings.Split(table, "\n")[1:] {
				fields := strings.Fields(line)
				count, err := strconv.Atoi(fields[len(fields)-1])
				if err != nil {
					t.Fatalf("invalid histogram count in %q: %v", line, err)
				}
				total += count
			}
			if total != len(test.values) {
				t.Errorf("histogram counts sum to %d, want %d", total, len(test.values))
			}
			if test.unit == "bytes/s" {
				checkReportText(t, got, "load [KiB/s]")
			}
		})
	}
}

// TestFormatOrderingAndManyObservations retains every observation in numeric counts.
func TestFormatOrderingAndManyObservations(t *testing.T) {
	var values []model.Value
	for range 65 {
		values = append(values, scalarResult(1))
	}
	got := Format(measurements.Report{"queries": map[string]measurements.QueryReport{
		"zulu":  {Observations: formatterObservations(scalarResult(2))},
		"alpha": {Observations: formatterObservations(values...)},
	}})
	if strings.Index(got, "\nalpha\n") > strings.Index(got, "\nzulu\n") {
		t.Errorf("queries are not sorted:\n%s", got)
	}
	checkReportText(t, got, "Attempts: 65", "Values: 65  No data: 0", "Attempts: 1", "Values: 1  No data: 0")
	var constantCounts []string
	for _, line := range strings.Split(got, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && (fields[0] == "1" || fields[0] == "2") {
			constantCounts = append(constantCounts, strings.Join(fields, " "))
		}
	}
	if got := strings.Join(constantCounts, ", "); got != "1 65, 2 1" {
		t.Errorf("constant histogram counts = %q, want all observations counted", got)
	}
	if strings.ContainsAny(got, "▁▂▃▄▅▆▇█") || strings.Contains(got, "Timeline:") || strings.Contains(got, "    Time ") {
		t.Errorf("constant series report contains a bar or timeline:\n%s", got)
	}
}

// TestFormatFallback renders other implementations and empty findings.
func TestFormatFallback(t *testing.T) {
	if got := Format(nil); got != "No measurements." {
		t.Errorf("empty report = %q", got)
	}
	if got := Format(measurements.Report{"count": 3}); got != "{\n  \"count\": 3\n}" {
		t.Errorf("generic report = %q", got)
	}
}

// TestFormatHistogramAlignment checks column positions for short and wide ranges.
func TestFormatHistogramAlignment(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []float64
	}{
		{name: "constant", values: []float64{42, 42}},
		{name: "varying", values: []float64{170, 190, 210}},
		{name: "negative", values: []float64{-60, -20, 3}},
		{name: "precise", values: []float64{1, math.Nextafter(1, 2)}},
		{name: "wide", values: []float64{-math.MaxFloat64, math.MaxFloat64}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var values []model.Value
			for _, value := range test.values {
				values = append(values, scalarResult(value))
			}
			got := Format(measurements.Report{"queries": map[string]measurements.QueryReport{
				"load": {Observations: formatterObservations(values...)},
			}})
			_, table, ok := strings.Cut(got, "    Range")
			if !ok {
				t.Fatalf("report lacks histogram:\n%s", got)
			}
			lines := strings.Split("    Range"+table, "\n")
			for _, line := range lines[1:] {
				if !strings.HasPrefix(line, "    ") || len(line) != len(lines[0]) {
					t.Errorf("histogram row does not align with its heading:\n%q\n%q", lines[0], line)
				}
			}
		})
	}
}

// TestFormatPreservesText leaves long queries and label sets intact for copying.
func TestFormatPreservesText(t *testing.T) {
	metric := model.Metric{
		"__name__":            "otelcol_process_memory_rss_bytes",
		"instance":            "9fc02c9d-713d-47f9-8478-7f40b3a70250",
		"job":                 "nktp-gateway-metrics",
		"service_instance_id": "9fc02c9d-713d-47f9-8478-7f40b3a70250",
		"service_name":        "nktp-gateway-metrics",
		"service_version":     "0.159.0",
	}
	observations := formatterObservations(model.Vector{&model.Sample{Metric: metric, Value: 114384896}})
	observations[0].Warnings = []string{"The query returned partial results because one or more backends were unavailable during evaluation"}
	start := observations[0].EvaluatedAt
	report := measurements.Report{
		"started_at": start, "finished_at": start.Add(3 * time.Minute), "interval": "15s",
		"queries": map[string]measurements.QueryReport{
			"gateway_memory_bytes": {
				Expression: `sum by (instance, service_instance_id) (otelcol_process_memory_rss_bytes{job="nktp-gateway-metrics"})`,
				Unit:       "bytes", Observations: observations,
			},
		},
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	got := Format(report)
	query := report["queries"].(map[string]measurements.QueryReport)["gateway_memory_bytes"]
	checkReportText(t, got, "  Query: "+query.Expression+"\n", "  Series: "+metric.String()+"\n")
	checkReportText(t, got, `instance="9fc02c9d-713d-47f9-8478-7f40b3a70250"`,
		`service_instance_id="9fc02c9d-713d-47f9-8478-7f40b3a70250"`, `service_version="0.159.0"`,
		"Warning at 2026-09-17T15:04:00Z:", "Values: 1", "Min: 109.086", "Observations")
	after, err := json.Marshal(report)
	if err != nil || string(before) != string(after) {
		t.Errorf("formatting changed the raw report, marshal error = %v", err)
	}
	t.Log("\n" + got)
}
