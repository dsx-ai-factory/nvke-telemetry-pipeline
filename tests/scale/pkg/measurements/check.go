// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"fmt"
	"math"
	"time"

	"github.com/prometheus/common/model"
)

// CheckReport retains the action, hold duration and matching series.
// A nil FiredAt means the check did not fire during collection.
type CheckReport struct {
	Action  Action         `json:"action"`
	For     time.Duration  `json:"for"`
	FiredAt *time.Time     `json:"fired_at,omitempty"`
	Series  []model.Metric `json:"series,omitempty"`
}

// checkState tracks each series independently within one measurement run.
type checkState struct {
	// complete requires one finite aggregate series for a completion target.
	complete bool
	// hold is the required duration across consecutive matching evaluations.
	hold time.Duration
	// pending maps complete label sets to their first matching evaluation.
	pending map[string]time.Time
}

// evaluate rejects backend warnings, returns firing series and resets timers
// for series no longer present. Vector membership signals a violation,
// regardless of the sample's value.
func (s *checkState) evaluate(observation QueryObservation) ([]model.Metric, error) {
	if len(observation.Warnings) > 0 {
		return nil, fmt.Errorf("check response contains warnings: %v", observation.Warnings)
	}
	vector, ok := observation.Value.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("check expression must return an instant vector, got %T", observation.Value)
	}
	if s.complete {
		if len(vector) > 1 {
			return nil, fmt.Errorf("completion check must return at most one series, got %d", len(vector))
		}
		if len(vector) == 1 {
			sample := vector[0]
			if sample.Histogram != nil || math.IsNaN(float64(sample.Value)) || math.IsInf(float64(sample.Value), 0) {
				return nil, fmt.Errorf("completion check must return a finite float sample")
			}
		}
	}
	at := observation.EvaluatedAt
	current := make(map[string]time.Time, len(vector))
	var fired []model.Metric
	for _, sample := range vector {
		labels := sample.Metric.String()
		since, ok := s.pending[labels]
		if !ok {
			since = at
		}
		current[labels] = since
		if at.Sub(since) >= s.hold {
			fired = append(fired, sample.Metric)
		}
	}
	s.pending = current
	return fired, nil
}
