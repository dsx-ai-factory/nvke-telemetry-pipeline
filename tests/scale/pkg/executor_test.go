// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generatorFunc implements a generator with test-controlled behavior.
type generatorFunc func(context.Context) error

// Run executes the configured generation behavior.
func (f generatorFunc) Run(ctx context.Context) error { return f(ctx) }

// measurementFunc implements a measurement with test-controlled behavior.
type measurementFunc func(context.Context) (measurements.Report, error)

// Run executes the configured measurement behavior.
func (f measurementFunc) Run(ctx context.Context) (measurements.Report, error) { return f(ctx) }

// CompletesStage keeps ordinary test measurements independent of generator lifetime.
func (measurementFunc) CompletesStage() bool { return false }

// TestStageScheduling checks parallel components, sequential stages and ordered reports.
func TestStageScheduling(t *testing.T) {
	for _, last := range []string{"generator", "measurement"} {
		t.Run(last+" finishes last", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				started := [3]bool{}
				cleaned, nextStarted := false, false
				stages := []preparedStage{
					{
						name: "first", measurementType: "test",
						generators: []preparedGenerator{
							{name: "slow", generator: generatorFunc(func(context.Context) error {
								started[0] = true
								if last == "generator" {
									<-release
								}
								// Cleanup is part of the generator lifetime.
								time.Sleep(time.Second)
								cleaned = true
								return nil
							})},
							{name: "fast", generator: generatorFunc(func(context.Context) error {
								started[1] = true
								return nil
							})},
						},
						measurement: measurementFunc(func(context.Context) (measurements.Report, error) {
							started[2] = true
							if last == "measurement" {
								<-release
							}
							return measurements.Report{"samples": 2}, nil
						}),
					},
					{
						name: "second", measurementType: "test",
						measurement: measurementFunc(func(context.Context) (measurements.Report, error) {
							nextStarted = true
							assert.True(t, cleaned, "next stage started before generator cleanup")
							return measurements.Report{}, nil
						}),
					},
				}
				var reports []StageReport
				var err error
				done := make(chan struct{})
				go func() {
					reports, err = executeStages(t.Context(), slog.Default(), stages)
					close(done)
				}()
				synctest.Wait()
				assert.Equal(t, [3]bool{true, true, true}, started)
				assert.False(t, nextStarted)
				close(release)
				<-done
				require.NoError(t, err)
				assert.True(t, nextStarted)
				require.Len(t, reports, 2)
				assert.Equal(t, "first", reports[0].Name)
				assert.Equal(t, "second", reports[1].Name)
				assert.Equal(t, []GeneratorReport{{Name: "slow"}, {Name: "fast"}}, reports[0].Generators)
				assert.Equal(t, measurements.Report{"samples": 2}, reports[0].Report)
			})
		})
	}
}

// TestStageFailureStopsPlan checks peer cancellation, cleanup and partial findings.
func TestStageFailureStopsPlan(t *testing.T) {
	for _, source := range []string{"generator", "measurement"} {
		t.Run(source, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				failure := errors.New("component failed")
				started := make(chan struct{}, 3)
				release := make(chan struct{})
				cleaned := [2]bool{}
				nextStarted := false
				stages := []preparedStage{
					{
						name: "failing", measurementType: "test",
						generators: []preparedGenerator{
							{name: "load", generator: generatorFunc(func(ctx context.Context) error {
								started <- struct{}{}
								<-release
								if source == "generator" {
									return failure
								}
								<-ctx.Done()
								cleaned[0] = true
								return ctx.Err()
							})},
							{name: "peer", generator: generatorFunc(func(ctx context.Context) error {
								started <- struct{}{}
								<-ctx.Done()
								time.Sleep(time.Second)
								cleaned[1] = true
								return ctx.Err()
							})},
						},
						measurement: measurementFunc(func(ctx context.Context) (measurements.Report, error) {
							started <- struct{}{}
							<-release
							report := measurements.Report{"samples": 2}
							if source == "measurement" {
								return report, failure
							}
							<-ctx.Done()
							cleaned[0] = true
							return report, ctx.Err()
						}),
					},
					{name: "unstarted", generators: []preparedGenerator{
						{name: "load", generator: generatorFunc(func(context.Context) error {
							nextStarted = true
							return nil
						})},
					}},
				}
				var reports []StageReport
				var err error
				done := make(chan struct{})
				go func() {
					reports, err = executeStages(t.Context(), slog.Default(), stages)
					close(done)
				}()
				for range 3 {
					<-started
				}
				close(release)
				<-done
				assert.ErrorIs(t, err, failure)
				assert.ErrorIs(t, err, context.Canceled)
				assert.Equal(t, [2]bool{true, true}, cleaned)
				assert.False(t, nextStarted)
				require.Len(t, reports, 1)
				assert.Equal(t, 2, reports[0].Report["samples"])
				assert.ErrorIs(t, reports[0].Generators[1].Error, context.Canceled)
			})
		})
	}
}

// TestPlanCancellation checks active components stop and later stages stay unstarted.
func TestPlanCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := [2][2]bool{}
		stages := make([]preparedStage, 2)
		for i, name := range []string{"first", "second"} {
			stages[i] = preparedStage{
				name: name, measurementType: "test",
				generators: []preparedGenerator{
					{name: "load", generator: generatorFunc(func(ctx context.Context) error {
						started[i][0] = true
						<-ctx.Done()
						return ctx.Err()
					})},
				},
				measurement: measurementFunc(func(ctx context.Context) (measurements.Report, error) {
					started[i][1] = true
					<-ctx.Done()
					return measurements.Report{"partial": true}, ctx.Err()
				}),
			}
		}
		var reports []StageReport
		var err error
		done := make(chan struct{})
		go func() {
			reports, err = executeStages(ctx, slog.Default(), stages)
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, [2][2]bool{{true, true}, {}}, started)
		require.Len(t, reports, 1)
		assert.ErrorIs(t, reports[0].Generators[0].Error, context.Canceled)
		assert.ErrorIs(t, reports[0].MeasurementError, context.Canceled)
		assert.Equal(t, true, reports[0].Report["partial"])
	})
}

// TestIgnoredDeadlineCannotPass checks a runnable returning nil after cancellation.
func TestIgnoredDeadlineCannotPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		stage := preparedStage{
			name: "late",
			generators: []preparedGenerator{
				{name: "load", generator: generatorFunc(func(context.Context) error {
					time.Sleep(2 * time.Second)
					return nil
				})},
			},
		}
		_, err := executeStages(ctx, slog.Default(), []preparedStage{stage})
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// completionFunc supplies a completion target for lifecycle tests.
type completionFunc func(context.Context) (measurements.Report, error)

// Run returns the test-controlled target outcome.
func (f completionFunc) Run(ctx context.Context) (measurements.Report, error) { return f(ctx) }

// CompletesStage requires generators to stay active until this target is reached.
func (completionFunc) CompletesStage() bool { return true }

// TestStageTargetShutdown waits for cleanup and never hides errors behind success.
func TestStageTargetShutdown(t *testing.T) {
	failure := errors.New("cleanup failed")
	for _, test := range []struct {
		name           string
		generatorError error
		cancelCaller   bool
		wantError      error
	}{
		{name: "clean shutdown"},
		{name: "cleanup failed", generatorError: errors.Join(context.Canceled, failure), wantError: failure},
		{name: "caller canceled during cleanup", cancelCaller: true, wantError: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := make(chan struct{}, 2)
				cleaned := [2]bool{}
				nextStarted := false
				stage := preparedStage{name: "warmup", measurement: completionFunc(func(context.Context) (measurements.Report, error) {
					<-started
					<-started
					return measurements.Report{"ready": true}, nil
				})}
				for i := range 2 {
					stage.generators = append(stage.generators, preparedGenerator{generator: generatorFunc(func(ctx context.Context) error {
						started <- struct{}{}
						<-ctx.Done()
						assert.ErrorIs(t, context.Cause(ctx), lifecycle.ErrStageComplete)
						time.Sleep(time.Second)
						cleaned[i] = true
						if test.cancelCaller {
							cancel()
						}
						return test.generatorError
					})})
				}
				later := preparedStage{name: "stress", generators: []preparedGenerator{{generator: generatorFunc(func(context.Context) error {
					nextStarted = true
					assert.Equal(t, [2]bool{true, true}, cleaned)
					return nil
				})}}}
				reports, err := executeStages(ctx, slog.Default(), []preparedStage{stage, later})
				assert.ErrorIs(t, err, test.wantError)
				assert.Equal(t, test.wantError == nil, nextStarted)
				assert.Equal(t, [2]bool{true, true}, cleaned)
				assert.Equal(t, true, reports[0].Report["ready"])
			})
		})
	}
}

// TestStageRejectsEarlyGeneratorCompletion prevents targets reached after load ends.
func TestStageRejectsEarlyGeneratorCompletion(t *testing.T) {
	stage := preparedStage{
		name:       "warmup",
		generators: []preparedGenerator{{generator: generatorFunc(func(context.Context) error { return nil })}},
		measurement: completionFunc(func(ctx context.Context) (measurements.Report, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}
	_, err := executeStages(t.Context(), slog.Default(), []preparedStage{stage})
	assert.ErrorContains(t, err, "generator finished before the completion target")
}
