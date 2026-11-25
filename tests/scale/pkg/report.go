// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"io"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
)

// WriteReport encodes plan inputs, observations and execution errors as JSON.
func WriteReport(w io.Writer, source []byte, args map[string]string, report PlanReport, runErr error) error {
	type generator struct {
		Name  string `json:"name"`
		Error string `json:"error,omitempty"`
	}
	type stage struct {
		Name        string              `json:"name"`
		Generators  []generator         `json:"generators"`
		Measurement string              `json:"measurement,omitempty"`
		Report      measurements.Report `json:"report"`
		Error       string              `json:"measurement_error,omitempty"`
	}
	output := struct {
		Version     int               `json:"version"`
		Plan        string            `json:"plan"`
		Description string            `json:"description,omitempty"`
		RunID       string            `json:"run_id"`
		Source      string            `json:"plan_source"`
		Arguments   map[string]string `json:"arguments"`
		Stages      []stage           `json:"stages"`
		Error       string            `json:"error,omitempty"`
	}{
		Version: 1, Plan: report.Plan, Description: report.Description, RunID: report.RunID,
		Source: string(source), Arguments: args, Stages: []stage{},
		Error: errorText(runErr),
	}
	for _, s := range report.Stages {
		saved := stage{
			Name: s.Name, Measurement: s.Measurement, Report: s.Report,
			Error: errorText(s.MeasurementError), Generators: []generator{},
		}
		for _, g := range s.Generators {
			saved.Generators = append(saved.Generators, generator{Name: g.Name, Error: errorText(g.Error)})
		}
		output.Stages = append(output.Stages, saved)
	}
	return json.NewEncoder(w).Encode(output)
}

// errorText preserves errors that encoding/json would otherwise turn into objects.
func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
