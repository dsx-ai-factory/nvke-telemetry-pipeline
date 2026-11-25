// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Action selects the outcome of a sustained check.
type Action string

const (
	// Fail stops the plan when a failure condition holds.
	Fail Action = "fail"
	// Complete stops the stage's generators and advances after cleanup.
	Complete Action = "complete"
)

// Measurement names either a reporting query or a check.
type Measurement struct {
	Name  string `yaml:"name"`
	Units string `yaml:"units,omitempty"`
	Query string `yaml:"query,omitempty"`
	Check *Check `yaml:"check,omitempty"`
}

// Check acts when a PromQL result contains a series for the specified duration.
// Expr must return an instant vector. Action defaults to Fail and For to immediate.
type Check struct {
	Description string        `yaml:"description,omitempty"`
	Expr        string        `yaml:"expr"`
	For         time.Duration `yaml:"for,omitempty"`
	Action      Action        `yaml:"action,omitempty"`
}

// validateMeasurements checks entry fields before decoding can coerce scalar types.
// Names are unique within the component, and omitted units leave values unscaled.
func validateMeasurements(node yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return errors.New("measurements must be a sequence")
	}
	names := make(map[string]bool, len(node.Content))
	for i, entry := range node.Content {
		if entry.Kind != yaml.MappingNode {
			return fmt.Errorf("measurements[%d] must be a mapping", i)
		}
		fields := make(map[string]string, 4)
		for j := 0; j < len(entry.Content); j += 2 {
			key, value := entry.Content[j], entry.Content[j+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("measurements[%d] field names must be strings", i)
			}
			switch key.Value {
			case "name", "units", "query", "check":
			default:
				return fmt.Errorf("measurements[%d] has unknown field %q", i, key.Value)
			}
			if _, ok := fields[key.Value]; ok {
				return fmt.Errorf("measurements[%d] has duplicate field %q", i, key.Value)
			}
			if key.Value == "check" {
				if err := validateCheck(*value); err != nil {
					return fmt.Errorf("measurements[%d].check: %w", i, err)
				}
				fields[key.Value] = ""
				continue
			}
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
				return fmt.Errorf("measurements[%d].%s must be a nonempty string", i, key.Value)
			}
			fields[key.Value] = value.Value
		}
		name := fields["name"]
		_, hasQuery := fields["query"]
		_, hasCheck := fields["check"]
		if name == "" || hasQuery == hasCheck {
			return fmt.Errorf("measurements[%d] requires name and exactly one of query or check", i)
		}
		if names[name] {
			return fmt.Errorf("duplicate measurement name %q", name)
		}
		names[name] = true
	}
	return nil
}

// validateCheck rejects coercion and unsupported options before decoding a check.
func validateCheck(node yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("must be a mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag != "!!str" || (key.Value != "expr" && key.Value != "for" && key.Value != "action" && key.Value != "description") {
			return fmt.Errorf("unknown check field %q", key.Value)
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
			return fmt.Errorf("%s must be a nonempty string", key.Value)
		}
	}
	var check Check
	if err := node.Decode(&check); err != nil {
		return err
	}
	if check.Action != "" && check.Action != Fail && check.Action != Complete {
		return errors.New("action must be fail or complete")
	}
	if check.Expr == "" || check.For < 0 {
		return errors.New("expr is required and for must not be negative")
	}
	return nil
}
