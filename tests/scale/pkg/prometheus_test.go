// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/format"
	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestExecutePrometheusPlan exercises argument binding, construction and reporting.
func TestExecutePrometheusPlan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm(), "ParseForm()")
		if got, want := r.Form.Get("query"), `count({test_run_id="integration-run"})`; got != want {
			t.Errorf("query = %q, want resolved run ID in %q", got, want)
		}
		_, err := fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[100,"3"]}]}}`)
		assert.NoError(t, err)
	}))
	defer server.Close()
	data := []byte(fmt.Sprintf(`name: query-integration
timeout: 5s
stages:
  - name: metrics
    generators:
      - {name: load, type: pod, spec: {count: 0}}
    measurement:
      type: prometheus
      spec:
        endpoint: %q
        duration: 200ms
        interval: 15ms
        measurements:
          - name: series
            units: series
            query: count({test_run_id="{{ .Args.runId }}"})
`, server.URL))
	args, err := ParseArguments([]string{"run-id=integration-run"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Parse(data, args)
	if err != nil {
		t.Fatal(err)
	}
	report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if report.RunID != "integration-run" || len(report.Stages) != 1 {
		t.Fatalf("report = %+v, want one stage with the supplied run ID", report)
	}
	stage := report.Stages[0]
	if stage.Name != "metrics" || stage.Measurement != "prometheus" {
		t.Errorf("stage attribution = %+v, want named Prometheus measurement", stage)
	}
	queries := stage.Report["queries"].(map[string]measurements.QueryReport)
	observations := queries["series"].Observations
	if len(observations) < 2 || observations[0].Value.(model.Vector)[0].Value != 3 {
		t.Errorf("observations = %+v, want continued polling after the empty Pod batch finishes", observations)
	}
	formatted := format.Format(stage.Report)
	for _, want := range []string{"series [series]", "Min: 3", "Mean: 3", "Max: 3", "Last: 3", "No data: 0", "Range ", "Observations"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatted report lacks %q:\n%s", want, formatted)
		}
	}
	if strings.ContainsAny(formatted, "▁▂▃▄▅▆▇█") || strings.Contains(formatted, "Timeline:") || strings.Contains(formatted, "    Time ") {
		t.Errorf("formatted report contains a bar or timeline:\n%s", formatted)
	}
	if strings.Contains(queries["series"].Expression, "{{ .Args.") {
		t.Errorf("report retains an unresolved expression: %s", queries["series"].Expression)
	}
}

// TestPrometheusCheckStopsPlan exercises querying, partial reports and peer cancellation.
func TestPrometheusCheckStopsPlan(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-started
		assert.NoError(t, r.ParseForm())
		switch r.Form.Get("query") {
		case "sum(rate(points_total[1m]))":
			_, err := fmt.Fprint(w, `{"status":"success","data":{"resultType":"scalar","result":[100,"25"]}}`)
			assert.NoError(t, err)
		case "up == 0":
			_, err := fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"instance":"gateway"},"value":[100,"0"]}]}}`)
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected query %q", r.Form.Get("query"))
		}
	}))
	defer server.Close()
	var spec yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(fmt.Sprintf(`endpoint: %q
duration: 5s
interval: 10ms
measurements:
  - name: throughput
    query: sum(rate(points_total[1m]))
  - name: unavailable
    check:
      expr: up == 0
      for: 20ms
`, server.URL)), &spec))
	measurement, err := measurements.NewPrometheus(*spec.Content[0])
	require.NoError(t, err)
	stages := []preparedStage{
		{
			name: "load", measurementType: "prometheus", measurement: measurement,
			generators: []preparedGenerator{{name: "producer", generator: generatorFunc(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			})}},
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reports, err := executeStages(ctx, slog.Default(), stages)
	require.ErrorContains(t, err, `check "unavailable" fired`)
	require.Len(t, reports, 1)
	assert.ErrorIs(t, reports[0].Generators[0].Error, context.Canceled)
	queries := reports[0].Report["queries"].(map[string]measurements.QueryReport)
	require.NotEmpty(t, queries["throughput"].Observations)
	check := queries["unavailable"]
	require.GreaterOrEqual(t, len(check.Observations), 2)
	require.NotNil(t, check.Check.FiredAt)
	assert.GreaterOrEqual(t, check.Check.FiredAt.Sub(check.Observations[0].EvaluatedAt), 20*time.Millisecond)
	assert.Equal(t, []model.Metric{{"instance": "gateway"}}, check.Check.Series)
	formatted := format.Format(reports[0].Report)
	assert.Contains(t, formatted, "throughput")
	assert.Contains(t, formatted, "Check: up == 0")
	assert.Contains(t, formatted, "FIRED at")
	assert.Contains(t, formatted, `Series: {instance="gateway"}`)
}

// TestPrometheusCompletionAdvancesStages exercises warmup, baseline and stress in order.
func TestPrometheusCompletionAdvancesStages(t *testing.T) {
	started := make(chan struct{})
	cleaned := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-started
		_, err := fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[100,"1"]}]}}`)
		assert.NoError(t, err)
	}))
	defer server.Close()
	var spec yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(fmt.Sprintf(`endpoint: %q
duration: 2s
interval: 5ms
measurements:
  - name: ready
    check: {expr: "sum(up) > 0", for: 10ms, action: complete}
`, server.URL)), &spec))
	warmup, err := measurements.NewPrometheus(*spec.Content[0])
	require.NoError(t, err)
	baseline, err := measurements.NewPrometheus(*spec.Content[0])
	require.NoError(t, err)
	stages := []preparedStage{
		{name: "warmup", measurement: warmup, generators: []preparedGenerator{
			{name: "load", generator: generatorFunc(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				cleaned = true
				return ctx.Err()
			})},
		}},
		{name: "baseline", measurement: baseline},
		{name: "stress", generators: []preparedGenerator{
			{name: "load", generator: generatorFunc(func(context.Context) error {
				assert.True(t, cleaned, "warmup cleanup must finish before stress starts")
				return nil
			})},
		}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reports, err := executeStages(ctx, slog.Default(), stages)
	require.NoError(t, err)
	require.Len(t, reports, 3)
	for _, report := range reports[:2] {
		check := report.Report["queries"].(map[string]measurements.QueryReport)["ready"]
		require.NotNil(t, check.Check.FiredAt)
		assert.GreaterOrEqual(t, check.Check.FiredAt.Sub(check.Observations[0].EvaluatedAt), 10*time.Millisecond)
		assert.Contains(t, format.Format(report.Report), "Action: complete")
	}
	assert.NoError(t, reports[0].Generators[0].Error)
}
