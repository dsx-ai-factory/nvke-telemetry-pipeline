// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/generators"
	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
)

// newGenerator selects a generator constructor that validates its spec.
func newGenerator(config ComponentSpec, kubeconfig string) (generator, error) {
	switch config.Type {
	case "kubernetes":
		return generators.NewKubernetesGenerator(config.Spec, kubeconfig)
	case "pod":
		return generators.NewPodGenerator(config.Spec, kubeconfig)
	case "statefulset":
		return generators.NewStatefulSetGenerator(config.Spec, kubeconfig)
	case "job":
		return generators.NewJobGenerator(config.Spec, kubeconfig)
	default:
		return nil, fmt.Errorf("unknown type %q", config.Type)
	}
}

// newMeasurement selects a measurement constructor that validates its spec.
func newMeasurement(config ComponentSpec) (measurement, error) {
	switch config.Type {
	case "prometheus":
		return measurements.NewPrometheus(config.Spec)
	default:
		return nil, fmt.Errorf("unknown type %q", config.Type)
	}
}
