// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tests

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/engine"
	"sigs.k8s.io/yaml"
)

var update = flag.Bool("update", false, "rewrite golden output files")

func render(t *testing.T, valuesPath string) (string, error) {
	t.Helper()
	chart, err := loader.Load("test-chart")
	require.NoError(t, err)
	library, err := loader.Load("../chart")
	require.NoError(t, err)
	chart.SetDependencies(library)

	data, err := os.ReadFile(valuesPath)
	require.NoError(t, err)
	var input, original any
	require.NoError(t, yaml.Unmarshal(data, &input))
	require.NoError(t, yaml.Unmarshal(data, &original))
	rendered, err := engine.Render(chart, common.Values{"Values": input})
	assert.Equal(t, original, input, "NVCB must not mutate its input")
	if err != nil {
		return "", err
	}

	require.Len(t, rendered, 1, "fixture chart must render exactly one template")
	for source, manifest := range rendered {
		// Retain Helm's source header and framing in the existing golden files.
		return fmt.Sprintf("---\n# Source: %s\n%s\n", source, strings.TrimSpace(manifest)), nil
	}
	t.Fatal("fixture chart rendered no templates")
	return "", nil
}

func TestPass(t *testing.T) {
	scenarios, err := os.ReadDir("pass")
	require.NoError(t, err)
	for _, scenario := range scenarios {
		t.Run(scenario.Name(), func(t *testing.T) {
			manifest, err := render(t, filepath.Join("pass", scenario.Name(), "values.yaml"))
			require.NoError(t, err)

			goldenPath := filepath.Join("pass", scenario.Name(), "output.yaml")
			if *update {
				require.NoError(t, os.WriteFile(goldenPath, []byte(manifest), 0o644))
				return
			}
			expected, err := os.ReadFile(goldenPath)
			require.NoError(t, err)
			assert.Equal(t, string(expected), manifest,
				"rendered output does not match %s (run go test -update to regenerate)", goldenPath)
		})
	}
}

func TestFail(t *testing.T) {
	scenarios, err := os.ReadDir("fail")
	require.NoError(t, err)
	for _, scenario := range scenarios {
		t.Run(scenario.Name(), func(t *testing.T) {
			_, err := render(t, filepath.Join("fail", scenario.Name(), "values.yaml"))
			require.Error(t, err, "expected configuration to be rejected")

			goldenPath := filepath.Join("fail", scenario.Name(), "error.txt")
			expected, readErr := os.ReadFile(goldenPath)
			require.NoError(t, readErr)

			assert.ErrorContains(t, err, strings.TrimSpace(string(expected)),
				"error does not contain expected message from %s", goldenPath)
		})
	}
}
