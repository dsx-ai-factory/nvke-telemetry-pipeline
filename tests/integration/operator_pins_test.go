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
// falls back to, and every Target Allocator image in the default render name
// the same release. It reads the built chart dependencies.
func TestOperatorPinsMatchChart(t *testing.T) {
	archives, err := filepath.Glob("../../nktp/charts/opentelemetry-kube-stack-*.tgz")
	require.NoError(t, err)
	require.Len(t, archives, 1, "build the chart dependencies first: helm dependency build nktp")
	charts, err := readChartFiles(archives[0], "opentelemetry-kube-stack/Chart.yaml", "opentelemetry-kube-stack/charts/opentelemetry-operator/Chart.yaml")
	require.NoError(t, err)
	stack, operator := charts[0].AppVersion, charts[1].AppVersion
	require.NotEmpty(t, operator, "bundled Operator appVersion")
	assert.Equal(t, operator, stack, "kube-stack appVersion must name the bundled Operator for the Target Allocator fallback")

	rendered := command(t, "helm", "template", "pins", "../../nktp")
	images := regexp.MustCompile(`/target-allocator:(\S+)`).FindAllSubmatch(rendered, -1)
	require.NotEmpty(t, images, "Target Allocator images in the default render")
	for _, image := range images {
		assert.Equal(t, operator, string(image[1]), "rendered Target Allocator tag must match the bundled Operator")
	}

	// Output excludes standard error, where go reports module downloads.
	module, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Version}}", "github.com/open-telemetry/opentelemetry-operator/apis").Output()
	require.NoError(t, err, "go list")
	assert.Equal(t, "v"+operator, strings.TrimSpace(string(module)), "tests/go.mod Operator API must match the bundled Operator")
}

type chartFile struct {
	AppVersion string `yaml:"appVersion"`
}

// readChartFiles reads Chart.yaml files from a packaged chart, in order.
func readChartFiles(archive string, names ...string) (_ []chartFile, err error) {
	file, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	found := map[string]chartFile{}
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
		found[header.Name] = chart
	}
	charts := make([]chartFile, len(names))
	for i, name := range names {
		chart, ok := found[name]
		if !ok {
			return nil, fmt.Errorf("%s has no %s", archive, name)
		}
		charts[i] = chart
	}
	return charts, nil
}
