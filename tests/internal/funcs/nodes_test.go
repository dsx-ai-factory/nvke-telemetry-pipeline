// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package funcs

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const gpu corev1.ResourceName = "nvidia.com/gpu"

func node(name string, allocatable corev1.ResourceList) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Allocatable: allocatable},
	}
}

func TestNodesWithAllocatable(t *testing.T) {
	whole := corev1.ResourceList{gpu: resource.MustParse("8")}
	// The mixed MIG strategy advertises each MIG profile and reports zero whole GPUs.
	mig := corev1.ResourceList{gpu: resource.MustParse("0"), "nvidia.com/mig-1g.24gb": resource.MustParse("7")}
	cpuOnly := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16")}

	tests := []struct {
		name       string
		nodes      []corev1.Node
		candidates []string
		want       []string
	}{
		{
			name:       "no GPU nodes",
			nodes:      []corev1.Node{node("cpu-a", cpuOnly), node("cpu-b", cpuOnly)},
			candidates: []string{"cpu-a", "cpu-b"},
		},
		{
			name:       "every GPU MIG-partitioned",
			nodes:      []corev1.Node{node("mig-a", mig), node("mig-b", mig)},
			candidates: []string{"mig-a", "mig-b"},
		},
		{
			name:       "MIG and whole-GPU nodes",
			nodes:      []corev1.Node{node("mig-a", mig), node("gpu-a", whole), node("gpu-b", whole)},
			candidates: []string{"mig-a", "gpu-a", "gpu-b"},
			want:       []string{"gpu-a", "gpu-b"},
		},
		{
			name:       "whole GPUs",
			nodes:      []corev1.Node{node("gpu-a", whole), node("gpu-b", whole)},
			candidates: []string{"gpu-b", "gpu-a"},
			want:       []string{"gpu-b", "gpu-a"},
		},
		{
			name:       "only candidates are returned",
			nodes:      []corev1.Node{node("gpu-a", whole), node("gpu-b", whole)},
			candidates: []string{"gpu-b"},
			want:       []string{"gpu-b"},
		},
		{
			name:       "candidate without a node",
			nodes:      []corev1.Node{node("gpu-a", whole)},
			candidates: []string{"gone", "gpu-a"},
			want:       []string{"gpu-a"},
		},
		{
			name:       "no candidates",
			nodes:      []corev1.Node{node("gpu-a", whole)},
			candidates: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NodesWithAllocatable(tt.nodes, tt.candidates, gpu))
		})
	}
}
