// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package scale

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runner "github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg"
	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/format"
)

// planPath selects a YAML plan relative to tests/scale or by absolute path.
var planPath = flag.String("plan", "", "required scale test plan file")

// kubeconfigPath selects one file for every Kubernetes-backed generator in the plan.
var kubeconfigPath = flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig file (default: KUBECONFIG)")

// argumentValues holds the raw repeated flags so each TestRun gets a fresh default.
var argumentValues []string

// init registers repeatable argument flags for resolution in each TestRun.
func init() {
	flag.Func("arg", "plan argument NAME=VALUE (repeatable, run-id defaults to a random ID)", func(value string) error {
		argumentValues = append(argumentValues, value)
		return nil
	})
}

// TestRun loads a plan and logs consolidated reports after every stage completes.
func TestRun(t *testing.T) {
	if *planPath == "" {
		t.Fatal("-plan is required; for example: task scale:test -- -plan examples/telemetrygen/plan.yaml")
	}
	args, err := runner.ParseArguments(argumentValues)
	if err != nil {
		t.Fatalf("Parse arguments: %v", err)
	}
	kubeconfig, err := resolveKubeconfig(*kubeconfigPath, flag.Args())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(*planPath)
	if err != nil {
		t.Fatalf("read plan %q: %v", *planPath, err)
	}
	plan, err := runner.Parse(data, args)
	if err != nil {
		t.Fatalf("parse plan %q: %v", *planPath, err)
	}
	report, err := (runner.Executor{Kubeconfig: kubeconfig}).Execute(t.Context(), plan, args["runId"])
	t.Logf("\nplan=%s run=%s stages=%d", report.Plan, report.RunID, len(report.Stages))
	for _, stage := range report.Stages {
		t.Logf("\nstage=%s", stage.Name)
		for _, generator := range stage.Generators {
			t.Logf("generator=%s error=%v", generator.Name, generator.Error)
		}
		if stage.Measurement != "" {
			t.Logf("\nmeasurement=%s error=%v\n\n%s",
				stage.Measurement, stage.MeasurementError, format.Format(stage.Report))
		}
	}
	if err != nil {
		t.Errorf("execute plan %q: %v", plan.Name, err)
	}
}

// resolveKubeconfig also accepts the trailing KUBECONFIG=path form forwarded by
// Task after --. Other positional arguments and multi-file values are errors.
func resolveKubeconfig(path string, args []string) (string, error) {
	for _, arg := range args {
		value, ok := strings.CutPrefix(arg, "KUBECONFIG=")
		if !ok || value == "" {
			return "", fmt.Errorf("unexpected argument %q: use -kubeconfig PATH or KUBECONFIG=PATH", arg)
		}
		path = value
	}
	if path == "" {
		return "", fmt.Errorf("set KUBECONFIG or pass -kubeconfig")
	}
	if paths := filepath.SplitList(path); len(paths) > 1 {
		return "", fmt.Errorf("kubeconfig %q lists multiple files: pass one path", path)
	}
	return filepath.Abs(path)
}
