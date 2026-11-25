// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package funcs

import (
	corev1 "k8s.io/api/core/v1"
)

// NodesWithAllocatable returns the names in candidates whose node reports more
// than zero allocatable of resource, in the order of candidates. A name with no
// matching node is left out.
//
// A node whose GPUs are all MIG-partitioned under the mixed strategy reports
// nvidia.com/gpu: 0, so a pod that requests nvidia.com/gpu cannot be scheduled
// there even though dcgm-exporter runs on it.
func NodesWithAllocatable(nodes []corev1.Node, candidates []string, resource corev1.ResourceName) []string {
	allocatable := make(map[string]bool, len(nodes))
	for i := range nodes {
		quantity, ok := nodes[i].Status.Allocatable[resource]
		allocatable[nodes[i].Name] = ok && quantity.Sign() > 0
	}
	var names []string
	for _, name := range candidates {
		if allocatable[name] {
			names = append(names, name)
		}
	}
	return names
}
