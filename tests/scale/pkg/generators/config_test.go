// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"testing"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func parseSpec(t *testing.T, data string) yaml.Node {
	t.Helper()
	var document yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(data), &document))
	return *document.Content[0]
}

func TestDecodeSpec(t *testing.T) {
	for _, test := range []struct {
		name      string
		spec      string
		want      podConfig
		wantError bool
	}{
		{name: "preserve defaults", spec: "{}", want: podConfig{Count: 1}},
		{
			name: "native JSON fields",
			spec: "count: 3\npodTemplate: {spec: {terminationGracePeriodSeconds: 7}}",
			want: podConfig{Count: 3, PodTemplate: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{TerminationGracePeriodSeconds: new(int64(7))},
			}},
		},
		{name: "unknown native field", spec: "podTemplate: {spec: {imag: typo}}", wantError: true},
		{name: "duplicate field", spec: "count: 1\ncount: 2", wantError: true},
		{name: "wrong type", spec: "count: many", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := podConfig{Count: 1}
			err := decodeSpec(parseSpec(t, test.spec), &got)
			if test.wantError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestPreparePodTemplate(t *testing.T) {
	for _, test := range []struct {
		name          string
		namespace     string
		labels        map[string]string
		wantNamespace string
	}{
		{name: "defaults", wantNamespace: "default"},
		{name: "configured", namespace: "workloads", labels: map[string]string{"app": "load", generatorOwnerLabel: "previous"}, wantNamespace: "workloads"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: test.namespace, Labels: test.labels,
					Annotations: map[string]string{"example.com/note": "preserved"},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "load", Image: "example:test"}}},
			}
			original := source.DeepCopy()
			got := preparePodTemplate(source, "owner")
			assert.Equal(t, test.wantNamespace, got.Namespace)
			assert.Equal(t, "owner", got.Labels[generatorOwnerLabel])
			assert.Equal(t, test.labels["app"], got.Labels["app"])
			assert.Equal(t, source.Annotations, got.Annotations)
			assert.Equal(t, source.Spec, got.Spec)
			got.Labels["app"] = "changed"
			got.Annotations["example.com/note"] = "changed"
			got.Spec.Containers[0].Image = "changed"
			assert.Equal(t, *original, source, "prepared template must not share mutable fields with its source")
		})
	}
}

// TestRunInstancesSuccessfulStop avoids starting instances after the target is reached.
func TestRunInstancesSuccessfulStop(t *testing.T) {
	ctx, stop := context.WithCancelCause(t.Context())
	stop(lifecycle.ErrStageComplete)
	err := runInstances(ctx, 3, func(context.Context) error {
		assert.Fail(t, "instance started after successful stop")
		return nil
	})
	assert.NoError(t, err)
}
