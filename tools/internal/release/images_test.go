// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"strings"
	"testing"
)

const manifests = `kind: Deployment
metadata:
  labels:
    app.kubernetes.io/name: opentelemetry-operator
spec:
  template:
    spec:
      containers:
        - image: ghcr.io/test/operator:123.1.0
          args:
            - --collector-image=ghcr.io/test/fallback-collector:123.2.0
            - --target-allocator-image
            - ghcr.io/test/allocator:123.3.0
---
kind: OpenTelemetryCollector
metadata:
  name: example
spec:
  image: ghcr.io/test/collector:123.4.0
  initContainers:
    - image: ghcr.io/test/collector:123.4.0
  targetAllocator:
    enabled: true
  config:
    attributes:
      image: ignored.invalid/config:fixture
---
kind: Pod
metadata:
  labels:
    app.kubernetes.io/name: opentelemetry-operator
  annotations:
    helm.sh/hook: test
spec:
  containers:
    - image: busybox
---
kind: Job
metadata:
  annotations:
    helm.sh/hook: pre-delete
spec:
  template:
    spec:
      containers:
        - image: example.com/test/kubectl:fixture
---
kind: Job
metadata:
  labels:
    app.kubernetes.io/name: nktp-namespace-labeling
  annotations:
    helm.sh/hook: pre-install, pre-upgrade
spec:
  template:
    spec:
      containers:
        - image: example.com/test/setup:fixture
`

func TestDiscoverEffectiveImages(t *testing.T) {
	images, err := discover([]byte(manifests))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 5 {
		t.Fatalf("expected five release images, got %v", images)
	}
	for image, uses := range map[string][]string{
		"ghcr.io/test/operator:123.1.0":    {"Operator"},
		"ghcr.io/test/collector:123.4.0":   {"Collector", "Collector sidecar"},
		"ghcr.io/test/allocator:123.3.0":   {"Target Allocator"},
		"example.com/test/setup:fixture":   {"Runs before install or upgrade when `namespace.labeling.enabled=true`"},
		"example.com/test/kubectl:fixture": {"Helm hook: pre-delete"},
	} {
		for _, use := range uses {
			if !images[image][use] {
				t.Errorf("missing %s use %q", image, use)
			}
		}
	}
	if strings.Contains(markdown(images), "fallback-collector") || strings.Contains(markdown(images), "ignored.invalid") {
		t.Fatal("unused defaults or configuration attributes entered the inventory")
	}
	if images["docker.io/library/busybox:latest"] != nil {
		t.Fatal("test-only hook entered the release inventory")
	}
}

func TestDiscoverFallbacksAndOverrides(t *testing.T) {
	withoutCollectorImage := strings.Replace(manifests, "  image: ghcr.io/test/collector:123.4.0\n", "", 1)
	images, err := discover([]byte(withoutCollectorImage))
	if err != nil || !images["ghcr.io/test/fallback-collector:123.2.0"]["Collector"] {
		t.Fatalf("Operator Collector default was not used: %v, %v", images, err)
	}
	explicitAllocator := strings.Replace(manifests, "    enabled: true", "    enabled: true\n    image: example.com/custom/allocator:fixture", 1)
	images, err = discover([]byte(explicitAllocator))
	if err != nil || !images["example.com/custom/allocator:fixture"]["Target Allocator"] {
		t.Fatalf("explicit Allocator image did not win: %v, %v", images, err)
	}
	if images["ghcr.io/test/allocator:123.3.0"] != nil {
		t.Fatal("unused Allocator default entered the inventory")
	}
	disabled := strings.Replace(explicitAllocator, "enabled: true", "enabled: false", 1)
	images, err = discover([]byte(disabled))
	if err != nil || images["example.com/custom/allocator:fixture"] != nil {
		t.Fatalf("disabled Allocator entered the inventory: %v, %v", images, err)
	}
}
