// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestOperatorPinsMatchChart checks that the tests' Operator API module, the
// bundled Operator, the kube-stack appVersion that the Target Allocator tag
// falls back to, and every rendered Target Allocator image name the same
// release, with the bundled Operator enabled and disabled. It reads the built
// chart dependencies.
func TestOperatorPinsMatchChart(t *testing.T) {
	const (
		stackPath    = "opentelemetry-kube-stack/Chart.yaml"
		operatorPath = "opentelemetry-kube-stack/charts/opentelemetry-operator/Chart.yaml"
	)
	archives, err := filepath.Glob("../../nktp/charts/opentelemetry-kube-stack-*.tgz")
	require.NoError(t, err)
	require.Len(t, archives, 1, "build the chart dependencies first: helm dependency build nktp")
	charts, err := readChartFiles(archives[0], stackPath, operatorPath)
	require.NoError(t, err)
	stack, operator := charts[stackPath].AppVersion, charts[operatorPath].AppVersion
	require.NotEmpty(t, operator, "bundled Operator appVersion")
	assert.Equal(t, operator, stack, "kube-stack appVersion must name the bundled Operator for the Target Allocator fallback")

	allocatorImage := regexp.MustCompile(`/target-allocator:(\S+)`)
	for name, args := range map[string][]string{
		"bundled Operator":  nil,
		"disabled Operator": {"--set", "opentelemetry-kube-stack.opentelemetry-operator.enabled=false"},
	} {
		t.Run(name, func(t *testing.T) {
			rendered := command(t, "helm", append([]string{"template", "pins", "../../nktp"}, args...)...)
			images := allocatorImage.FindAllSubmatch(rendered, -1)
			require.NotEmpty(t, images, "Target Allocator images in the render")
			for _, image := range images {
				assert.Equal(t, operator, string(image[1]), "rendered Target Allocator tag must match the bundled Operator")
			}
		})
	}

	// Output excludes standard error, where go reports module downloads.
	module, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Version}}", "github.com/open-telemetry/opentelemetry-operator/apis").Output()
	require.NoError(t, err, "go list")
	assert.Equal(t, "v"+operator, strings.TrimSpace(string(module)), "tests/go.mod Operator API must match the bundled Operator")
}

type chartFile struct {
	AppVersion string `yaml:"appVersion"`
}

// readChartFiles reads the named Chart.yaml files from a packaged chart.
func readChartFiles(archive string, names ...string) (_ map[string]chartFile, err error) {
	file, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	charts := map[string]chartFile{}
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(names, header.Name) {
			continue
		}
		var chart chartFile
		if err := yaml.NewDecoder(reader).Decode(&chart); err != nil {
			return nil, fmt.Errorf("%s: %w", header.Name, err)
		}
		charts[header.Name] = chart
	}
	for _, name := range names {
		if _, ok := charts[name]; !ok {
			return nil, fmt.Errorf("%s has no %s", archive, name)
		}
	}
	return charts, nil
}
