// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChartReferenceBoundaries guards against corrupting unrelated version strings.
func TestChartReferenceBoundaries(t *testing.T) {
	original := "0.2.0 0.2.0 nktp/v0.2.0-rc.1 `v0.2.0` 10.2.0 0.2.00 name0.2.0 0.2.0.1 0.2.0.example\n" +
		"Install chart 0.2.0. Next sentence (v0.2.0.)\nInstall chart 0.2.0."
	want := "0.3.0 0.3.0 nktp/v0.3.0-rc.1 `v0.3.0` 10.2.0 0.2.00 name0.2.0 0.2.0.1 0.2.0.example\n" +
		"Install chart 0.3.0. Next sentence (v0.3.0.)\nInstall chart 0.3.0."
	if got := replaceVersion(original, "0.2.0", "0.3.0"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlanAndApplyReleaseDocumentation(t *testing.T) {
	root := t.TempDir()
	dependencyTable := beginDependencies + "\n\n" +
		"| Chart | Version | Installs | Enabled by default | Values key |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| prometheus-node-exporter | stale | node-exporter | Yes | `node-exporter` |\n\n" + endDependencies
	files := map[string]string{
		"README.md":                                 "Install chart 0.2.0 and nktp/v0.2.0-rc.1\n",
		"nktp/README.md":                            "Upgrade chart 0.2.0\n" + dependencyTable + "\n" + beginInventory + "\nold table\n" + endInventory + "\nfooter\n",
		"docs/guides/use_blueprints.md":             "targetRevision: nktp/v0.2.0\n",
		"docs/guides/operator.md":                   "[Operator v123.1.0](https://github.com/open-telemetry/opentelemetry-operator/blob/v123.1.0/docs/README.md#tls)\n",
		"nktp/Chart.yaml":                           "version: 0.2.0\ndependencies:\n  - name: prometheus-node-exporter\n    alias: node-exporter\n    version: 0.2.0\n",
		"nktp/Chart.lock":                           "version: 0.2.0\n",
		"nktp/values.yaml":                          "blueprint: {version: 0.2.0}\n",
		"tests/go.sum":                              "example.com/dependency v0.2.0 fake-checksum\n",
		"RELEASING.md":                              "Illustrative release nktp/v0.2.0\n",
		"nktp/THIRD_PARTY_NOTICES.md":               "Dependency notices 0.2.0\n",
		"docs/components/dependencies.md":           "Dependency update policy for 0.2.0\n",
		".github/workflows/dependabot-notices.yaml": "# Workflow-owned data stays unchanged\n",
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A container version matching the old chart version must survive regeneration.
	table := "| Full image reference | Use |\n| --- | --- |\n| `example.com/image:0.2.0` | Fixture |\n"
	images := ResolvedImages{Table: table, operatorImages: []string{"example.com/operator:123.2.0"}}
	refreshChanges, err := Plan(root, "", "", images)
	if err != nil || len(refreshChanges) != 2 {
		t.Fatalf("expected image-table and Operator-link updates: %v, %v", refreshChanges, err)
	}
	for _, change := range refreshChanges {
		switch change.Path {
		case filepath.Join(root, "nktp/README.md"):
			if !strings.Contains(string(change.Data), "Upgrade chart 0.2.0\n") || !strings.Contains(string(change.Data), table) {
				t.Fatal("default refresh changed chart references or missed the image table")
			}
			if !strings.Contains(string(change.Data), "| prometheus-node-exporter | 0.2.0 | node-exporter | Yes | `node-exporter` |") {
				t.Fatal("dependency version did not follow Chart.yaml")
			}
		case filepath.Join(root, "docs/guides/operator.md"):
			if !strings.Contains(string(change.Data), "Operator v123.2.0") || !strings.Contains(string(change.Data), "/blob/v123.2.0/") {
				t.Fatal("default refresh missed Operator references")
			}
		default:
			t.Fatalf("default refresh changed an unrelated file: %s", change.Path)
		}
	}
	for _, versions := range [][2]string{{"", ""}, {"0.2.0", "0.3.0"}} {
		if _, err := Plan(root, versions[0], versions[1], ResolvedImages{Table: table}); err == nil {
			t.Fatal("documentation refresh accepted an unresolved Operator version")
		}
	}
	for _, versions := range [][2]string{{"0.2.0", ""}, {"", "0.3.0"}} {
		if _, err := Plan(root, versions[0], versions[1], images); err == nil {
			t.Fatal("accepted an incomplete chart version pair")
		}
	}
	changes, err := Plan(root, "0.2.0", "0.3.0", images)
	if err != nil || len(changes) != 4 {
		t.Fatalf("unexpected plan: %v, %v", changes, err)
	}
	if err := Apply(changes, true); err == nil {
		t.Fatal("check mode accepted stale documentation")
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("planning or check mode changed %s", name)
		}
	}
	if err := Apply(changes, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "nktp/README.md", "docs/guides/use_blueprints.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !strings.Contains(string(data), "0.3.0") {
			t.Fatalf("reference was not updated in %s", name)
		}
		delete(files, name)
	}
	operatorDoc, err := os.ReadFile(filepath.Join(root, "docs/guides/operator.md"))
	if err != nil || string(operatorDoc) != "[Operator v123.2.0](https://github.com/open-telemetry/opentelemetry-operator/blob/v123.2.0/docs/README.md#tls)\n" {
		t.Fatalf("Operator references did not follow resolved image: %s, %v", operatorDoc, err)
	}
	delete(files, "docs/guides/operator.md")
	readme, err := os.ReadFile(filepath.Join(root, "nktp/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "example.com/image:0.2.0") || !strings.HasSuffix(string(readme), "footer\n") {
		t.Fatal("generation changed an image version or surrounding prose")
	}
	if !strings.Contains(string(readme), "| prometheus-node-exporter | 0.2.0 |") {
		t.Fatal("chart-reference replacement changed the dependency version")
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("unrelated file changed: %s", name)
		}
	}
	changes, err = Plan(root, "0.2.0", "0.3.0", images)
	if err != nil || len(changes) != 0 {
		t.Fatalf("repeated preparation is not idempotent: %v, %v", changes, err)
	}
}

func TestReplaceOperatorReferences(t *testing.T) {
	for _, original := range []string{
		"[mounts TLS](https://github.com/open-telemetry/opentelemetry-operator/blob/v123.1.0/docs/target-allocator/README.md#without-cert-manager)",
		"https://github.com/open-telemetry/opentelemetry-operator/tree/v123.1.0/docs",
		"https://raw.githubusercontent.com/open-telemetry/opentelemetry-operator/v123.1.0/docs/README.md",
		"[Operator v123.1.0 release notes](https://github.com/open-telemetry/opentelemetry-operator/releases/tag/v123.1.0)",
		"https://github.com/open-telemetry/opentelemetry-operator/releases/tag/v123.1.0\n",
	} {
		want := strings.ReplaceAll(original, "123.1.0", "123.2.0")
		if got := replaceOperatorReferences(original, "123.2.0"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	unchanged := "Operator v123.1.0 prose; dependency 123.1.0\n" +
		"https://github.com/open-telemetry/opentelemetry-operator/blob/main/CHANGELOG.md\n" +
		"https://github.com/open-telemetry/opentelemetry-operator/blob/ca0b832a4cb071d144d374b453d9560b77a7d8d7/pkg/sidecar/pod.go#L79\n" +
		"https://github.com/other/operator/blob/v123.1.0/docs/README.md\n" +
		"https://github.com/open-telemetry/opentelemetry-operator/blob/v123.1.0-example_branch/docs\n"
	if got := replaceOperatorReferences(unchanged, "123.2.0"); got != unchanged {
		t.Fatalf("unrelated reference changed: %q", got)
	}
}
