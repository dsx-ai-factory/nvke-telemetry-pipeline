// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
)

// generator owns a complete generation lifetime. Run is called once per instance,
// must honor cancellation, and must finish any owned goroutines and bounded
// cleanup before returning, including after partial startup. Nil means success.
type generator interface {
	Run(context.Context) error
}

// measurement observes and evaluates a stage concurrently with its generators.
// Run is called once per instance, must honor cancellation, and must finish its
// goroutines and bounded cleanup before returning. It may return a partial report
// with an error. When CompletesStage is true, Run succeeds only after reaching
// its target and generators must remain active until then.
type measurement interface {
	Run(context.Context) (measurements.Report, error)
	CompletesStage() bool
}

// StageReport attributes a measurement report and component errors to one stage.
// Errors remain available even when a measurement produces no report.
type StageReport struct {
	Name             string
	Measurement      string
	Report           measurements.Report
	Generators       []GeneratorReport
	MeasurementError error
}

// GeneratorReport attributes a generator's outcome to its configured name.
type GeneratorReport struct {
	Name  string
	Error error
}

// PlanReport contains started stages in declaration order.
type PlanReport struct {
	Plan   string
	RunID  string
	Stages []StageReport
}

// Executor validates and runs plans. Its zero value supports observation-only plans.
// Execute calls are independent and may run concurrently. Callers supply the
// run ID resolved during argument parsing. Callers must not modify a plan or
// its specs while it is being executed.
type Executor struct {
	// Kubeconfig is the absolute path to the file used by Kubernetes-backed generators.
	// It is required only for generators that access a cluster. Credentials are read in Run.
	Kubeconfig string
}

// Execute constructs all runnables before starting any stage, then runs them
// under the plan timeout. A stage failure cancels its peers and stops the plan.
// Cancellation stops unstarted stages and waits for started runnables to return.
// Reports, including partial findings, accompany execution errors. Invalid plans
// return an empty PlanReport. Completion uses the default slog logger.
// runID must match the ID retained by Parse when args contained runId.
// A mismatch returns an empty report before any component is constructed.
func (e Executor) Execute(ctx context.Context, plan Plan, runID string) (PlanReport, error) {
	if err := ctx.Err(); err != nil {
		return PlanReport{}, err
	}
	if err := validateRunID(runID); err != nil {
		return PlanReport{}, err
	}
	if plan.runID != "" && plan.runID != runID {
		return PlanReport{}, fmt.Errorf("run-id %q does not match parsed plan run-id %q", runID, plan.runID)
	}
	if err := plan.validate(); err != nil {
		return PlanReport{}, err
	}
	stages, err := prepareStages(plan.Stages, e.Kubeconfig)
	if err != nil {
		return PlanReport{}, err
	}
	logger := slog.With("plan", plan.Name, "run_id", runID)
	runCtx, cancel := context.WithTimeout(ctx, plan.Timeout)
	defer cancel()
	reports, err := executeStages(runCtx, logger, stages)
	logger.InfoContext(ctx, "plan completed", "stages", len(reports), "error", err)
	return PlanReport{Plan: plan.Name, RunID: runID, Stages: reports}, err
}

// preparedStage binds validated specifications to fresh, inert runnables.
type preparedStage struct {
	// name identifies the stage in reports and logs.
	name string
	// generators retain declaration order for reporting.
	generators []preparedGenerator
	// measurementType identifies the observer implementation when present.
	measurementType string
	// measurement is nil for a load-only stage.
	measurement measurement
}

// preparedGenerator binds a configured name to a validated generator.
type preparedGenerator struct {
	// name identifies the generator within its stage.
	name string
	// generator owns the workload and its cleanup.
	generator generator
}

// prepareStages validates every implementation before execution acquires resources.
func prepareStages(stages []Stage, kubeconfig string) ([]preparedStage, error) {
	prepared := make([]preparedStage, 0, len(stages))
	for _, stage := range stages {
		p := preparedStage{name: stage.Name}
		for _, spec := range stage.Generators {
			g, err := newGenerator(spec.ComponentSpec, kubeconfig)
			if err != nil {
				return nil, fmt.Errorf("stage %q generator %q: %w", stage.Name, spec.Name, err)
			}
			p.generators = append(p.generators, preparedGenerator{name: spec.Name, generator: g})
		}
		if stage.Measurement != nil {
			m, err := newMeasurement(*stage.Measurement)
			if err != nil {
				return nil, fmt.Errorf("stage %q measurement: %w", stage.Name, err)
			}
			p.measurementType, p.measurement = stage.Measurement.Type, m
		}
		prepared = append(prepared, p)
	}
	return prepared, nil
}

// executeStages runs stages in order and stops after the first failed stage.
// Only stages that started are included in the report.
func executeStages(ctx context.Context, logger *slog.Logger, stages []preparedStage) ([]StageReport, error) {
	reports := make([]StageReport, 0, len(stages))
	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return reports, err
		}
		report := executeStage(ctx, logger, stage)
		reports = append(reports, report)
		var errs []error
		for _, g := range report.Generators {
			if g.Error != nil {
				errs = append(errs, fmt.Errorf("stage %q generator %q: %w", report.Name, g.Name, g.Error))
			}
		}
		if report.MeasurementError != nil {
			errs = append(errs, fmt.Errorf("stage %q measurement: %w", report.Name, report.MeasurementError))
		}
		err := errors.Join(append(errs, ctx.Err())...)
		logger.InfoContext(ctx, "stage completed", "stage", stage.name, "error", err)
		if err != nil {
			return reports, err
		}
	}
	return reports, ctx.Err()
}

// executeStage runs all components to completion, canceling peers on error.
// A reached measurement target requests successful generator shutdown. Generators
// that finish before a required target fail the stage. Each goroutine owns a
// separate result slot, and all resource cleanup finishes before the report returns.
func executeStage(ctx context.Context, logger *slog.Logger, stage preparedStage) StageReport {
	report := StageReport{
		Name: stage.name, Measurement: stage.measurementType,
		Generators: make([]GeneratorReport, len(stage.generators)),
	}
	stageCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	requiresTarget := stage.measurement != nil && stage.measurement.CompletesStage()
	if stageCtx.Err() == nil {
		logger.InfoContext(stageCtx, "stage started", "stage", stage.name)
	}
	var wg sync.WaitGroup
	for i, g := range stage.generators {
		report.Generators[i].Name = g.name
		wg.Go(func() {
			err := stageCtx.Err()
			if err == nil {
				err = g.generator.Run(stageCtx)
			}
			if err == nil && requiresTarget && stageCtx.Err() == nil {
				err = errors.New("generator finished before the completion target")
			}
			report.Generators[i].Error = lifecycle.RunError(stageCtx, err)
			if report.Generators[i].Error != nil {
				cancel(err)
			}
		})
	}
	if stage.measurement != nil {
		wg.Go(func() {
			err := stageCtx.Err()
			if err == nil {
				report.Report, err = stage.measurement.Run(stageCtx)
			}
			report.MeasurementError = err
			if err != nil {
				cancel(err)
			} else if requiresTarget {
				cancel(lifecycle.ErrStageComplete)
			}
		})
	}
	wg.Wait()
	return report
}
