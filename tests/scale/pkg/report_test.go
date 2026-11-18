// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
)

// TestWriteReport preserves inputs, report data and errors at each scope.
func TestWriteReport(t *testing.T) {
	stage := StageReport{
		Name: "load", Measurement: "test",
		Generators:       []GeneratorReport{{Name: "source", Error: errors.New("cleanup failed")}},
		MeasurementError: errors.New("measurement failed"),
		Report: measurements.Report{
			"value": 0, "empty": []string{}, "missing": nil,
			"non_finite": model.SampleValue(math.NaN()),
			"queries": map[string]measurements.QueryReport{
				"ready": {Expression: "up", Check: &measurements.CheckReport{Action: measurements.Complete, For: "30m0s"}},
			},
		},
	}
	report := PlanReport{Plan: "load", RunID: "run-1", Stages: []StageReport{stage}}
	var output bytes.Buffer
	require.NoError(t, WriteReport(&output, []byte("name: load"), map[string]string{"runId": "run-1"}, report, errors.New("run failed")))
	require.JSONEq(t, `{
		"version": 1, "plan": "load", "run_id": "run-1",
		"plan_source": "name: load", "arguments": {"runId": "run-1"},
		"error": "run failed",
		"stages": [{
			"name": "load", "measurement": "test",
			"generators": [{"name": "source", "error": "cleanup failed"}],
			"measurement_error": "measurement failed",
			"report": {
				"value": 0, "empty": [], "missing": null, "non_finite": "NaN",
				"queries": {"ready": {
					"expression": "up", "observations": null,
					"check": {"action": "complete", "for": "30m0s"}
				}}
			}
		}]
	}`, output.String())
}

// TestWriteReportEmptyReport preserves null versus empty reports and omits errors on success.
func TestWriteReportEmptyReport(t *testing.T) {
	for _, test := range []struct {
		report measurements.Report
		want   string
	}{{nil, "null"}, {measurements.Report{}, "{}"}} {
		t.Run(test.want, func(t *testing.T) {
			stage := StageReport{Generators: []GeneratorReport{{Name: "source"}}, Report: test.report}
			var output bytes.Buffer
			require.NoError(t, WriteReport(&output, nil, nil, PlanReport{Stages: []StageReport{stage}}, nil))
			require.JSONEq(t, `{
				"version": 1, "plan": "", "run_id": "", "plan_source": "", "arguments": null,
				"stages": [{"name": "", "generators": [{"name": "source"}], "report": `+test.want+`}]
			}`, output.String())
		})
	}
}

// TestWriteReportFailure propagates write and encoding errors to the caller.
func TestWriteReportFailure(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		want := errors.New("disk full")
		require.ErrorIs(t, WriteReport(failedWriter{want}, nil, nil, PlanReport{}, nil), want)
	})
	t.Run("encoding", func(t *testing.T) {
		var output bytes.Buffer
		report := PlanReport{Stages: []StageReport{{Report: measurements.Report{"unsupported": make(chan int)}}}}
		var unsupported *json.UnsupportedTypeError
		require.ErrorAs(t, WriteReport(&output, nil, nil, report, nil), &unsupported)
	})
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }
