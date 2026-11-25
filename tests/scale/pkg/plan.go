// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package runner executes scale test plans and consolidates measurement reports.
package runner

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Plan describes sequential stages that share an execution deadline.
// Timeout must be positive.
type Plan struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description,omitempty"`
	Timeout     time.Duration `yaml:"timeout"`
	Stages      []Stage       `yaml:"stages"`

	// runID retains the validated argument supplied to Parse and is empty when omitted.
	runID string
}

// Stage runs its generators and optional measurement concurrently.
// Names must be nonempty and unique within the plan.
type Stage struct {
	Name        string          `yaml:"name"`
	Generators  []GeneratorSpec `yaml:"generators,omitempty"`
	Measurement *ComponentSpec  `yaml:"measurement,omitempty"`
}

// GeneratorSpec names a generator within its stage and optionally delays its start.
type GeneratorSpec struct {
	Name string `yaml:"name"`
	// StartDelay postpones Run from stage start and must not be negative.
	StartDelay    time.Duration `yaml:"start_delay,omitempty"`
	ComponentSpec `yaml:",inline"`
}

// ComponentSpec selects an implementation and preserves its configuration.
// The selected implementation validates Spec before any stage starts.
type ComponentSpec struct {
	Type string    `yaml:"type"`
	Spec yaml.Node `yaml:"spec"`
}

// Parse executes raw text as a Go template with arguments under .Args, then
// decodes exactly one YAML document with strict envelope fields. Template output
// must fit the surrounding YAML syntax and quoting.
// Parse validates and retains runId when supplied so Executor.Execute can
// reject mismatched attribution. It does not generate argument defaults or change
// args or data. It validates the plan structure.
// Executor.Execute validates component specs before execution.
func Parse(data []byte, args map[string]string) (Plan, error) {
	if runID, supplied := args[runIDArgument]; supplied {
		if err := validateRunID(runID); err != nil {
			return Plan{}, err
		}
	}
	resolved, err := bindArguments(string(data), args)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{runID: args[runIDArgument]}
	decoder := yaml.NewDecoder(strings.NewReader(resolved))
	decoder.KnownFields(true)
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode plan: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Plan{}, errors.New("plan must contain exactly one YAML document")
	}
	if err := plan.validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// validate checks the envelope for both parsed and programmatically built plans.
func (p Plan) validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("plan name is required")
	}
	if p.Timeout <= 0 {
		return errors.New("plan timeout must be positive")
	}
	if len(p.Stages) == 0 {
		return errors.New("plan must contain at least one stage")
	}
	names := make(map[string]bool, len(p.Stages))
	for i, stage := range p.Stages {
		if strings.TrimSpace(stage.Name) == "" {
			return fmt.Errorf("stages[%d].name is required", i)
		}
		if names[stage.Name] {
			return fmt.Errorf("duplicate stage name %q", stage.Name)
		}
		names[stage.Name] = true
		if len(stage.Generators) == 0 && stage.Measurement == nil {
			return fmt.Errorf("stage %q requires generators or a measurement", stage.Name)
		}
		generatorNames := make(map[string]bool, len(stage.Generators))
		for j, generator := range stage.Generators {
			if strings.TrimSpace(generator.Name) == "" {
				return fmt.Errorf("stage %q generators[%d].name is required", stage.Name, j)
			}
			if generatorNames[generator.Name] {
				return fmt.Errorf("stage %q has duplicate generator name %q", stage.Name, generator.Name)
			}
			generatorNames[generator.Name] = true
			if generator.StartDelay < 0 {
				return fmt.Errorf("stage %q generator %q start_delay must not be negative", stage.Name, generator.Name)
			}
			if generator.Type == "" {
				return fmt.Errorf("stage %q generator %q requires type", stage.Name, generator.Name)
			}
		}
		if stage.Measurement != nil && stage.Measurement.Type == "" {
			return fmt.Errorf("stage %q requires measurement.type", stage.Name)
		}
	}
	return nil
}
