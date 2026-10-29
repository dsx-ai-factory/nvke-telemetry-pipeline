// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package generators runs Kubernetes workloads for scale test plans.
package generators

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	k8syaml "sigs.k8s.io/yaml"
)

// generatorOwnerLabel identifies the invocation that owns a generated resource.
const generatorOwnerLabel = "scale.nktp.io/generator"

// decodeSpec uses Kubernetes JSON field names and rejects unknown or duplicate
// fields. The caller supplies a pointer to a config with its defaults applied.
func decodeSpec(spec yaml.Node, into any) error {
	data, err := yaml.Marshal(&spec)
	if err != nil {
		return fmt.Errorf("encode spec: %w", err)
	}
	return k8syaml.UnmarshalStrict(data, into)
}

// preparePodTemplate copies native fields before adding per-run ownership and
// a default namespace. It leaves the caller's template unchanged.
func preparePodTemplate(src corev1.PodTemplateSpec, owner string) corev1.PodTemplateSpec {
	template := src.DeepCopy()
	if template.Namespace == "" {
		template.Namespace = "default"
	}
	if template.Labels == nil {
		template.Labels = make(map[string]string)
	}
	template.Labels[generatorOwnerLabel] = owner
	return *template
}

// runInstances runs independent resource lifecycles and waits for their cleanup.
// An error cancels the remaining instances.
func runInstances(ctx context.Context, count int, run func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return lifecycle.RunError(ctx, err)
	}
	if count == 1 {
		return run(ctx)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, count)
	for i := range count {
		wg.Go(func() {
			if err := ctx.Err(); err != nil {
				errs[i] = lifecycle.RunError(ctx, err)
				return
			}
			errs[i] = run(ctx)
			if errs[i] != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
