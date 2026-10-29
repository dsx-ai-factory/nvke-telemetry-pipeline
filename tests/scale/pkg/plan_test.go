// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planYAML describes two sequential stages with empty Pod batches and queries.
const planYAML = `name: smoke
timeout: 1m
stages:
  - name: first
    generators:
      - {name: load, type: pod, spec: {count: 0}}
    measurement: {type: prometheus, spec: {endpoint: http://127.0.0.1:1, duration: 100ms, interval: 5ms, measurements: [{name: available, query: up}]}}
  - name: second
    generators:
      - {name: load, type: pod, spec: {count: 0}}
    measurement: {type: prometheus, spec: {endpoint: http://127.0.0.1:1, duration: 100ms, interval: 5ms, measurements: [{name: available, query: up}]}}
`

// TestParse checks named components and stage declaration order.
func TestParse(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, plan.Timeout)
	require.Len(t, plan.Stages, 2)
	require.Len(t, plan.Stages[0].Generators, 1)
	assert.Equal(t, "load", plan.Stages[0].Generators[0].Name)
	assert.Equal(t, "pod", plan.Stages[0].Generators[0].Type)
	assert.Equal(t, "second", plan.Stages[1].Name)
}

// TestParseRejectsInvalidDocuments checks syntax, envelope fields and topology.
func TestParseRejectsInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: "decode plan"},
		{name: "syntax", data: "stages: [", want: "decode plan"},
		{name: "unknown field", data: planYAML + "typo: true\n", want: "field typo"},
		{name: "old generator field", data: strings.Replace(planYAML, "generators:", "generator:", 1), want: "field generator"},
		{name: "duplicate key", data: planYAML + "name: duplicate\n", want: "already defined"},
		{name: "second document", data: planYAML + "---\nname: extra\n", want: "exactly one"},
		{name: "empty trailing document", data: planYAML + "---\n", want: "exactly one"},
		{name: "missing name", data: strings.Replace(planYAML, "name: smoke", "name: ''", 1), want: "name is required"},
		{name: "old execution field", data: planYAML + "execution: parallel\n", want: "field execution"},
		{name: "bad duration", data: strings.Replace(planYAML, "1m", "forever", 1), want: "decode plan"},
		{name: "zero timeout", data: strings.Replace(planYAML, "1m", "0s", 1), want: "timeout must be positive"},
		{name: "no stages", data: "name: empty\ntimeout: 1m\nstages: []\n", want: "at least one stage"},
		{name: "missing stage name", data: strings.Replace(planYAML, "name: first", "name: ''", 1), want: "stages[0].name"},
		{name: "duplicate stage name", data: strings.Replace(planYAML, "name: second", "name: first", 1), want: "duplicate stage"},
		{name: "missing generator name", data: strings.Replace(planYAML, "name: load", "name: ''", 1), want: "generators[0].name"},
		{name: "duplicate generator name", data: strings.Replace(planYAML, "generators:", "generators:\n      - {name: load, type: pod}", 1), want: "duplicate generator name"},
		{name: "missing generator type", data: strings.Replace(planYAML, "type: pod", "type: ''", 1), want: "requires type"},
		{name: "missing measurement type", data: strings.Replace(planYAML, "measurement: {type: prometheus, spec: {endpoint: http://127.0.0.1:1, duration: 100ms, interval: 5ms, measurements: [{name: available, query: up}]}}", "measurement: {}", 1), want: "measurement.type"},
		{name: "empty stage", data: "name: empty\ntimeout: 1m\nstages: [{name: empty}]\n", want: "requires generators or a measurement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.data), nil)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// TestExecuteValidatesComponents checks every reference before running a plan.
func TestExecuteValidatesComponents(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "generator type", data: strings.Replace(planYAML, "name: load, type: pod", "name: load, type: missing", 1), want: `generator "load": unknown type "missing"`},
		{name: "later stage", data: planYAML + "  - name: invalid\n    measurement: {type: missing}\n", want: `stage "invalid" measurement: unknown type "missing"`},
		{name: "extra option", data: strings.Replace(planYAML, "spec: {count: 0}", "spec: {rate: 2}", 1), want: "decode pod spec"},
		{name: "scalar spec", data: strings.Replace(planYAML, "spec: {count: 0}", "spec: 3", 1), want: "decode pod spec"},
		{name: "null measurement spec", data: strings.Replace(planYAML, "spec: {endpoint: http://127.0.0.1:1, duration: 100ms, interval: 5ms, measurements: [{name: available, query: up}]}", "spec: null", 1), want: "prometheus spec must be a mapping"},
		{name: "measurement extra option", data: strings.Replace(planYAML, "measurement: {type: prometheus, spec: {endpoint: http://127.0.0.1:1, duration: 100ms, interval: 5ms, measurements: [{name: available, query: up}]}}", "measurement: {type: prometheus, spec: {rate: '2'}}", 1), want: "field rate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Parse([]byte(tc.data), nil)
			require.NoError(t, err)
			report, err := (Executor{}).Execute(t.Context(), plan, "test-run")
			assert.ErrorContains(t, err, tc.want)
			assert.Equal(t, PlanReport{}, report)
		})
	}
}

// executionPlanYAML serves real Prometheus observations for executor tests.
func executionPlanYAML(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[100,"1"]}]}}`)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	return strings.ReplaceAll(planYAML, "http://127.0.0.1:1", server.URL)
}

// TestExecutePlan checks report identity and reuse of a parsed plan.
func TestExecutePlan(t *testing.T) {
	plan, err := Parse([]byte(executionPlanYAML(t)), nil)
	require.NoError(t, err)
	var previousID string
	for range 2 {
		args, err := ParseArguments(nil)
		require.NoError(t, err)
		report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
		require.NoError(t, err)
		assert.Equal(t, args[runIDArgument], report.RunID)
		assert.NotEqual(t, previousID, report.RunID)
		assert.Equal(t, "smoke", report.Plan)
		previousID = report.RunID
		require.Len(t, report.Stages, 2)
		for i, name := range []string{"first", "second"} {
			stage := report.Stages[i]
			assert.Equal(t, name, stage.Name)
			assert.Equal(t, []GeneratorReport{{Name: "load"}}, stage.Generators)
			assert.Equal(t, "prometheus", stage.Measurement)
			assert.NoError(t, stage.MeasurementError)
			queries := stage.Report["queries"].(map[string]measurements.QueryReport)
			assert.NotEmpty(t, queries["available"].Observations)
		}
	}
}

// TestOptionalComponents checks observation-only and load-only stages.
func TestOptionalComponents(t *testing.T) {
	plan, err := Parse([]byte(executionPlanYAML(t)), nil)
	require.NoError(t, err)
	plan.Stages[0].Generators = nil
	plan.Stages[1].Measurement = nil
	report, err := (Executor{}).Execute(t.Context(), plan, "test-run")
	require.NoError(t, err)
	require.Len(t, report.Stages, 2)
	assert.Empty(t, report.Stages[0].Generators)
	assert.Equal(t, "prometheus", report.Stages[0].Measurement)
	assert.Equal(t, []GeneratorReport{{Name: "load"}}, report.Stages[1].Generators)
	assert.Empty(t, report.Stages[1].Measurement)
}

// TestExecuteRejectsInvalidInput checks the public API without relying on parsing.
func TestExecuteRejectsInvalidInput(t *testing.T) {
	_, err := (Executor{}).Execute(t.Context(), Plan{}, "test-run")
	assert.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = (Executor{}).Execute(ctx, Plan{}, "test-run")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestExecuteUsesSuppliedRunID checks that reports retain the ID supplied during parsing.
func TestExecuteUsesSuppliedRunID(t *testing.T) {
	data := executionPlanYAML(t)
	args, err := ParseArguments([]string{"run-id=ci-123"})
	require.NoError(t, err)
	plan, err := Parse([]byte(data), args)
	require.NoError(t, err)
	for range 2 {
		report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
		require.NoError(t, err)
		assert.Equal(t, "ci-123", report.RunID)
	}
}

// TestExecuteRejectsInvalidRunID checks execution rejects missing or malformed IDs.
func TestExecuteRejectsInvalidRunID(t *testing.T) {
	plan, err := Parse([]byte(planYAML), nil)
	require.NoError(t, err)
	for _, id := range []string{"", "bad id"} {
		_, err := (Executor{}).Execute(t.Context(), plan, id)
		assert.ErrorContains(t, err, "run-id")
	}
}

// TestParseRejectsInvalidRunID checks direct callers cannot bind an invalid ID.
func TestParseRejectsInvalidRunID(t *testing.T) {
	for _, id := range []string{"", "bad id"} {
		t.Run("run-id="+id, func(t *testing.T) {
			_, err := Parse([]byte(planYAML), map[string]string{runIDArgument: id})
			assert.ErrorContains(t, err, "run-id")
		})
	}
}

// TestExecuteRejectsMismatchedRunID checks parsing binds a snapshot of the run ID.
func TestExecuteRejectsMismatchedRunID(t *testing.T) {
	data := executionPlanYAML(t)
	args := map[string]string{runIDArgument: "run-a"}
	plan, err := Parse([]byte(data), args)
	require.NoError(t, err)
	args[runIDArgument] = "run-b"
	report, err := (Executor{}).Execute(t.Context(), plan, args[runIDArgument])
	assert.ErrorContains(t, err, "does not match")
	assert.Equal(t, PlanReport{}, report)
	report, err = (Executor{}).Execute(t.Context(), plan, "run-a")
	require.NoError(t, err)
	assert.Equal(t, "run-a", report.RunID)
}
