// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestClientSideApply checks every rendered object against Kubernetes' annotation
// limit after adding the configuration annotation that kubectl apply stores.
// Native dependency CRDs are installed separately and are outside this check.
func TestClientSideApply(t *testing.T) {
	rendered := command(t, "helm", "template", "nktp", "../../nktp", "--namespace", "telemetry",
		"--set", "otel-lgtm.enabled=true")
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
	count := 0
	for {
		var resource unstructured.Unstructured
		err := decoder.Decode(&resource)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if len(resource.Object) == 0 {
			continue
		}
		count++
		t.Run(resource.GetKind()+"/"+resource.GetName(), func(t *testing.T) {
			require.NoError(t, validateClientSideApply(&resource))
		})
	}
	require.Positive(t, count, "the chart must render resources")
}

// validateClientSideApply follows kubectl's serialization: remove any previous
// last-applied value, encode the object, then store that encoding as an annotation.
func validateClientSideApply(resource *unstructured.Unstructured) error {
	annotations := resource.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	delete(annotations, corev1.LastAppliedConfigAnnotation)
	resource.SetAnnotations(annotations)
	configuration, err := runtime.Encode(unstructured.UnstructuredJSONScheme, resource)
	if err != nil {
		return err
	}
	annotations[corev1.LastAppliedConfigAnnotation] = string(configuration)
	return validation.ValidateAnnotationsSize(annotations)
}

// TestClientSideApplyAnnotationLimit covers byte accounting, existing annotations,
// replacement of the previous configuration, and JSON escaping overhead.
func TestClientSideApplyAnnotationLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		data        string
		annotations map[string]string
		wantErr     bool
	}{
		{name: "small", data: "dashboard"},
		{name: "oversized dashboard", data: strings.Repeat("x", 262144), wantErr: true},
		{name: "UTF-8 bytes", data: strings.Repeat("界", 90000), wantErr: true},
		{name: "JSON escaping", data: strings.Repeat("\"", 140000), wantErr: true},
		{name: "existing annotations", data: strings.Repeat("x", 180000),
			annotations: map[string]string{"other": strings.Repeat("y", 50000)}, wantErr: true},
		{name: "replace last applied", data: "dashboard",
			annotations: map[string]string{corev1.LastAppliedConfigAnnotation: strings.Repeat("x", 262144)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]interface{}{"name": "dashboards"},
				"data":     map[string]interface{}{"dashboard.json": tc.data},
			}}
			resource.SetAnnotations(tc.annotations)
			err := validateClientSideApply(resource)
			if tc.wantErr {
				require.ErrorContains(t, err, "262144")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
