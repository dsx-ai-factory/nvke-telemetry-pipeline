// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckValidation covers the query/check choice and supported check settings.
func TestCheckValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		entry string
	}{
		{name: "query and check", entry: "query: up\n    check: {expr: up == 0}"},
		{name: "empty query and check", entry: "query: ''\n    check: {expr: up == 0}"},
		{name: "missing expression", entry: "check: {for: 1m}"},
		{name: "null check", entry: "check: null"},
		{name: "negative hold", entry: "check: {expr: up == 0, for: -1s}"},
		{name: "invalid hold", entry: "check: {expr: up == 0, for: later}"},
		{name: "hold equals duration", entry: "check: {expr: up == 0, for: 1m}"},
		{name: "hold exceeds duration", entry: "check: {expr: up == 0, for: 2m}"},
		{name: "numeric expression", entry: "check: {expr: 1}"},
		{name: "multiple completion targets", entry: "check: {expr: up == 1, action: complete}\n  - name: other\n    check: {expr: up == 1, action: complete}"},
		{name: "unsupported action", entry: "check: {expr: up == 0, action: unknown}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := fmt.Sprintf("endpoint: http://localhost:9090\nduration: 1m\nmeasurements:\n  - name: unavailable\n    %s\n", test.entry)
			_, err := NewPrometheus(measurementSpec(t, spec))
			assert.Error(t, err)
		})
	}
	for _, hold := range []string{"", ", for: 0s", ", for: 1m"} {
		spec := fmt.Sprintf("endpoint: http://localhost:9090\nduration: 2m\nmeasurements:\n  - name: unavailable\n    check: {expr: up == 0%s}\n", hold)
		measurement, err := NewPrometheus(measurementSpec(t, spec))
		require.NoError(t, err)
		require.NotNil(t, measurement.config.Measurements[0].Check)
		assert.Equal(t, "up == 0", measurement.config.Measurements[0].Check.Expr)
		assert.Equal(t, Fail, measurement.config.Measurements[0].Check.Action)
	}
}

// TestCheckHoldAndReset keeps series timers independent and resets missing matches.
func TestCheckHoldAndReset(t *testing.T) {
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	first := &model.Sample{Metric: model.Metric{"instance": "gateway", "exporter": "first"}, Value: 0}
	second := &model.Sample{Metric: model.Metric{"instance": "gateway", "exporter": "second"}, Value: 1}
	state := checkState{hold: time.Minute}
	for _, test := range []struct {
		after time.Duration
		value model.Vector
		fired []model.Metric
	}{
		{value: model.Vector{first}},
		{after: 30 * time.Second, value: model.Vector{first, second}},
		{after: time.Minute, value: model.Vector{first, second}, fired: []model.Metric{first.Metric}},
		{after: 90 * time.Second, value: model.Vector{second}, fired: []model.Metric{second.Metric}},
		{after: 2 * time.Minute, value: model.Vector{}},
		{after: 3 * time.Minute, value: model.Vector{first}},
		{after: 4 * time.Minute, value: model.Vector{first}, fired: []model.Metric{first.Metric}},
	} {
		fired, err := state.evaluate(QueryObservation{Value: test.value, EvaluatedAt: at.Add(test.after)})
		require.NoError(t, err)
		assert.Equal(t, test.fired, fired, "evaluation at %s", test.after)
	}
}

// TestCheckImmediate rejects non-vector results and treats zero-valued matches as active.
func TestCheckImmediate(t *testing.T) {
	state := checkState{}
	at := time.Now()
	labels := model.Metric{"instance": "gateway"}
	fired, err := state.evaluate(QueryObservation{Value: model.Vector{&model.Sample{Metric: labels, Value: 0}}, EvaluatedAt: at})
	require.NoError(t, err)
	assert.Equal(t, []model.Metric{labels}, fired)
	_, err = state.evaluate(QueryObservation{Value: &model.Scalar{Value: 1}, EvaluatedAt: at})
	assert.ErrorContains(t, err, "instant vector")
}

// TestCompletionCheckData requires a single finite aggregate match for completion.
func TestCompletionCheckData(t *testing.T) {
	for _, test := range []struct {
		name      string
		vector    model.Vector
		wantError bool
	}{
		{name: "empty"},
		{name: "finite match", vector: model.Vector{&model.Sample{Value: 0}}},
		{name: "multiple series", vector: model.Vector{&model.Sample{}, &model.Sample{}}, wantError: true},
		{name: "NaN", vector: model.Vector{&model.Sample{Value: model.SampleValue(math.NaN())}}, wantError: true},
		{name: "infinity", vector: model.Vector{&model.Sample{Value: model.SampleValue(math.Inf(1))}}, wantError: true},
		{name: "histogram", vector: model.Vector{&model.Sample{Histogram: &model.SampleHistogram{}}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := checkState{complete: true}
			fired, err := state.evaluate(QueryObservation{Value: test.vector, EvaluatedAt: time.Now()})
			if test.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, fired, len(test.vector))
		})
	}
}

// TestCheckRejectsWarnings prevents partial results from completing a stage.
func TestCheckRejectsWarnings(t *testing.T) {
	state := checkState{complete: true}
	fired, err := state.evaluate(QueryObservation{
		Value:    model.Vector{&model.Sample{Value: 1}},
		Warnings: []string{"partial response"},
	})
	require.ErrorContains(t, err, "check response contains warnings: [partial response]")
	assert.Empty(t, fired)
}
