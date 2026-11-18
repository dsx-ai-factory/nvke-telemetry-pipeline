// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSnapshotNormalization checks that version normalization preserves other
// output and rejects malformed tags instead of hiding them in the snapshot.
func TestSnapshotNormalization(t *testing.T) {
	// Exercise semver normalization without referencing published image versions.
	const version = "123.456.789"

	for _, tc := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "chart version only",
			input: "# Source: nktp/templates/watchdog.yaml\n  helm.sh/chart: opentelemetry-kube-stack-" + version + "\n  name: nktp-watchdog\n",
			want:  "# Source: nktp/templates/watchdog.yaml\n  helm.sh/chart: opentelemetry-kube-stack-VERSION\n  name: nktp-watchdog\n",
		},
		{
			name:  "collector tag",
			input: "  image: ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:" + version + "\n  replicas: 2\n",
			want:  "  image: ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:VERSION\n  replicas: 2\n",
		},
		{
			name:  "quoted allocator tag",
			input: "  image: 'ghcr.io/open-telemetry/opentelemetry-operator/target-allocator:" + version + "'\n",
			want:  "  image: 'ghcr.io/open-telemetry/opentelemetry-operator/target-allocator:VERSION'\n",
		},
		{
			name:  "kube-state-metrics tag prefix",
			input: "  - image: registry.k8s.io/kube-state-metrics/kube-state-metrics:v" + version + "\n",
			want:  "  - image: registry.k8s.io/kube-state-metrics/kube-state-metrics:vVERSION\n",
		},
		{
			name:  "backend tag",
			input: "  image: grafana/otel-lgtm:" + version + "\n",
			want:  "  image: grafana/otel-lgtm:VERSION\n",
		},
		{
			name:  "unexpected repository remains visible",
			input: "  image: example.com/collector:" + version + "\n",
			want:  "  image: example.com/collector:" + version + "\n",
		},
		{
			name:    "invalid image tag",
			input:   "  image: ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:latest\n",
			wantErr: true,
		},
		{
			name:    "invalid chart version",
			input:   "  helm.sh/chart: opentelemetry-kube-stack-invalid\n",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "python3", "../../nktp/tests/normalize_snapshot.py")
			cmd.Stdin = strings.NewReader(tc.input)
			output, err := cmd.CombinedOutput()
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, string(output), "invalid dependency version")
				return
			}
			require.NoError(t, err, "%s", output)
			require.Equal(t, tc.want, string(output))
		})
	}
}
