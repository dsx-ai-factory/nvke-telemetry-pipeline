// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExamplePlans checks examples without contacting a cluster or reading credentials.
func TestExamplePlans(t *testing.T) {
	entries, err := os.ReadDir("../examples")
	require.NoError(t, err, "find examples")
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	require.Equal(t, []string{"avalanche", "kube-events", "kwok", "pod-churn", "stdout-logs", "telemetrygen", "watchdog"}, names)
	paths := []string{"../mixed-workload/plan.yaml", "../mixed-workload-demo/plan.yaml"}
	for _, name := range names {
		dir := filepath.Join("../examples", name)
		require.FileExists(t, filepath.Join(dir, "README.md"))
		paths = append(paths, filepath.Join(dir, "plan.yaml"))
	}
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			plan, err := Parse(data, map[string]string{runIDArgument: "example-run"})
			require.NoError(t, err, "Parse()")
			if filepath.Base(filepath.Dir(path)) == "telemetrygen" {
				require.Len(t, plan.Stages, 1)
				stage := plan.Stages[0]
				require.Len(t, stage.Generators, 3)
				for _, generator := range stage.Generators {
					var spec struct {
						PodTemplate struct {
							Spec struct {
								Containers []struct {
									Args []string `yaml:"args"`
								} `yaml:"containers"`
							} `yaml:"spec"`
						} `yaml:"podTemplate"`
					}
					require.NoError(t, generator.Spec.Decode(&spec), "Decode()")
					containers := spec.PodTemplate.Spec.Containers
					require.Len(t, containers, 1, "generator %q containers", generator.Name)
					assert.Contains(t, containers[0].Args, `--telemetry-attributes=test_run_id="example-run"`, "generator %q telemetry attribute", generator.Name)
				}
				require.NotNil(t, stage.Measurement)
				require.Equal(t, "prometheus", stage.Measurement.Type)
				var spec struct {
					Measurements []measurements.Measurement `yaml:"measurements"`
				}
				require.NoError(t, stage.Measurement.Spec.Decode(&spec))
				index := slices.IndexFunc(spec.Measurements, func(m measurements.Measurement) bool {
					return m.Name == "active_series"
				})
				require.GreaterOrEqual(t, index, 0, "active_series measurement")
				assert.Contains(t, spec.Measurements[index].Query, `test_run_id="example-run"`)
			}
			_, err = prepareStages(plan.Stages, "/example/kubeconfig")
			assert.NoError(t, err, "prepareStages()")
		})
	}
}
