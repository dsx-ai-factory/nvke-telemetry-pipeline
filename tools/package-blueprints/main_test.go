// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestCheck covers the rules check applies to each definition, including the
// template function allowlist.
func TestCheck(t *testing.T) {
	tests := map[string]struct {
		definition string
		err        string
	}{
		"allowed functions and actions": {definition: `
version: 1.2.0-rc.1
description: d
template: '{{ with .values.a }}{{ toJson . }}{{ end }}{{ .values.b | default "x" | quote }}{{ required "c" .values.c }}{{ if not .values.d }}{{ fail "d" }}{{ end }}{{ printf "%s" .forward }}'
`},
		"cluster lookup":        {definition: "version: 1.0.0\ndescription: d\ntemplate: '{{ lookup \"v1\" \"Secret\" \"ns\" \"name\" }}'", err: `function "lookup" not defined`},
		"chart template":        {definition: "version: 1.0.0\ndescription: d\ntemplate: '{{ include \"nktp.image\" . }}'", err: `function "include" not defined`},
		"non-reproducible time": {definition: "version: 1.0.0\ndescription: d\ntemplate: '{{ now }}'", err: `function "now" not defined`},
		"missing version":       {definition: "description: d\ntemplate: t", err: "version is required"},
		"missing description":   {definition: "version: 1.0.0\ntemplate: t", err: "description is required"},
		"version not semantic":  {definition: "version: 1.0\ndescription: d\ntemplate: t", err: `version "1.0" is not a semantic version`},
		"unsupported field":     {definition: "version: 1.0.0\ndescription: d\ntemplate: t\nparameters: {}", err: `unsupported field "parameters"`},
		"template not a string": {definition: "version: 1.0.0\ndescription: d\ntemplate: {a: b}", err: "template must be a string"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var document yaml.Node
			if err := yaml.Unmarshal([]byte(test.definition), &document); err != nil {
				t.Fatal(err)
			}
			err := check(document.Content[0])
			switch {
			case test.err == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case test.err != "" && (err == nil || !strings.Contains(err.Error(), test.err)):
				t.Fatalf("got error %v, want one containing %q", err, test.err)
			}
		})
	}
}
