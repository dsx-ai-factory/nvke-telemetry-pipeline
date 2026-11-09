// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestCleanupSelectorMatchesCollectors checks the assembled chart's cleanup
// selector against its Collector labels without pinning a dependency version.
func TestCleanupSelectorMatchesCollectors(t *testing.T) {
	rendered := command(t, "helm", "template", "cleanup", "../../nktp", "--namespace", "telemetry")
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
	collectors := make(map[string]string)
	var jobs []batchv1.Job
	for {
		var resource unstructured.Unstructured
		err := decoder.Decode(&resource)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch resource.GetKind() {
		case "OpenTelemetryCollector":
			label := resource.GetLabels()["helm.sh/chart"]
			require.NotEmpty(t, label, "Collector %s must have a cleanup label", resource.GetName())
			collectors[resource.GetName()] = label
		case "Job":
			if resource.GetAnnotations()["helm.sh/hook"] == "pre-delete" {
				var job batchv1.Job
				require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(resource.Object, &job))
				jobs = append(jobs, job)
			}
		}
	}
	require.Len(t, jobs, 1, "expected the upstream pre-delete cleanup Job")
	require.NotEmpty(t, collectors)
	require.Contains(t, collectors, "nktp-watchdog")
	containers := jobs[0].Spec.Template.Spec.Containers
	require.Len(t, containers, 1)
	args := containers[0].Args
	selectorIndex := slices.Index(args, "-l")
	require.NotEqual(t, -1, selectorIndex, "cleanup Job must select Collectors by label")
	require.Less(t, selectorIndex+1, len(args), "cleanup selector must have a value")
	selector := args[selectorIndex+1]
	for name, label := range collectors {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, "helm.sh/chart="+label, selector, "cleanup selector must match the Collector label")
		})
	}
}
