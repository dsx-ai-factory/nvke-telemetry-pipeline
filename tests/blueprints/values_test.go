// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package blueprints checks the blueprints NKTP publishes and packages them as
// the default blueprints in the chart's values.
package blueprints

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

var update = flag.Bool("update", false, "repackage the default blueprints in nktp/values.yaml")

const (
	blueprintsDir = "../../blueprints"
	valuesFile    = "../../nktp/values.yaml"
	beginMarker   = "# BEGIN DEFAULT BLUEPRINTS"
	endMarker     = "# END DEFAULT BLUEPRINTS"
)

// allowedFunctions are the functions a blueprint template may call besides Go's
// built-in template functions. Parsing checks only names, so these are stubs.
var allowedFunctions = template.FuncMap{
	"default":  stub,
	"fail":     stub,
	"quote":    stub,
	"required": stub,
	"toJson":   stub,
}

func stub(...any) any { return nil }

// semver matches a semantic version, such as 1.2.0 or 2.0.0-rc.1.
var semver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// TestValuesSection checks every blueprint file under blueprints/ and that the
// default blueprints in nktp/values.yaml are exactly their definitions. Run it
// with -update to repackage them.
func TestValuesSection(t *testing.T) {
	section := defaultBlueprints(t)
	data, err := os.ReadFile(valuesFile)
	require.NoError(t, err)
	values, err := replaceSection(string(data), section)
	require.NoError(t, err)
	if *update {
		require.NoError(t, os.WriteFile(valuesFile, []byte(values), 0o644))
		return
	}
	require.Equal(t, values, string(data),
		"nktp/values.yaml does not match blueprints/. Run `task blueprints` and commit the result.")
}

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
			require.NoError(t, yaml.Unmarshal([]byte(test.definition), &document))
			err := check(document.Content[0])
			if test.err == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.err)
		})
	}
}

// defaultBlueprints returns the default blueprints section: one blueprints map
// that holds every definition under blueprints/, sorted by kind and type.
func defaultBlueprints(t *testing.T) string {
	t.Helper()
	definitions := map[string]map[string]*yaml.Node{}
	err := filepath.WalkDir(blueprintsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".yaml" {
			return err
		}
		kind, name, definition, err := load(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if definitions[kind] == nil {
			definitions[kind] = map[string]*yaml.Node{}
		}
		definitions[kind][name] = definition
		return nil
	})
	require.NoError(t, err)

	kinds := &yaml.Node{Kind: yaml.MappingNode}
	for _, kind := range slices.Sorted(maps.Keys(definitions)) {
		types := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range slices.Sorted(maps.Keys(definitions[kind])) {
			types.Content = append(types.Content, scalar(name), definitions[kind][name])
		}
		kinds.Content = append(kinds.Content, scalar(kind), types)
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	require.NoError(t, encoder.Encode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("blueprints"), kinds}}))
	require.NoError(t, encoder.Close())
	return out.String()
}

// load reads a blueprint file, which must be blueprints/<kind>/<type>.yaml and
// define only blueprints.<kind>.<type>, and returns that definition.
func load(path string) (kind, name string, definition *yaml.Node, err error) {
	relative, err := filepath.Rel(blueprintsDir, path)
	if err != nil {
		return "", "", nil, err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 || (parts[0] != "receivers" && parts[0] != "exporters") {
		return "", "", nil, errors.New("must be at blueprints/receivers/<type>.yaml or blueprints/exporters/<type>.yaml")
	}
	kind, name = parts[0], strings.TrimSuffix(parts[1], ".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", "", nil, err
	}
	if len(document.Content) != 1 {
		return "", "", nil, errors.New("must contain one YAML document")
	}
	node := document.Content[0]
	for _, key := range []string{"blueprints", kind, name} {
		if node.Kind != yaml.MappingNode || len(node.Content) != 2 || node.Content[0].Value != key {
			return "", "", nil, fmt.Errorf("must define only blueprints.%s.%s", kind, name)
		}
		node = node.Content[1]
	}
	if err := check(node); err != nil {
		return "", "", nil, fmt.Errorf("blueprints.%s.%s: %w", kind, name, err)
	}
	return kind, name, node, nil
}

// check validates a definition: a template that calls only allowed functions,
// a semantic version, and a description, and no other fields.
func check(definition *yaml.Node) error {
	if definition.Kind != yaml.MappingNode {
		return errors.New("must be a map")
	}
	fields := map[string]string{}
	for i := 0; i+1 < len(definition.Content); i += 2 {
		key, value := definition.Content[i].Value, definition.Content[i+1]
		if key != "template" && key != "version" && key != "description" {
			return fmt.Errorf("unsupported field %q: use template, version, and description", key)
		}
		if value.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s must be a string", key)
		}
		fields[key] = value.Value
	}
	for _, key := range []string{"template", "version", "description"} {
		if strings.TrimSpace(fields[key]) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	if !semver.MatchString(fields["version"]) {
		return fmt.Errorf("version %q is not a semantic version", fields["version"])
	}
	if _, err := template.New("template").Funcs(allowedFunctions).Parse(fields["template"]); err != nil {
		return err
	}
	return nil
}

// replaceSection returns values with the lines between the markers replaced by
// section.
func replaceSection(values, section string) (string, error) {
	begin := strings.Index(values, beginMarker+"\n")
	end := strings.Index(values, endMarker+"\n")
	if begin < 0 || end < begin || strings.Count(values, beginMarker) != 1 || strings.Count(values, endMarker) != 1 {
		return "", fmt.Errorf("%s must contain one %q line followed by one %q line", valuesFile, beginMarker, endMarker)
	}
	start := begin + len(beginMarker) + 1
	return values[:start] + section + values[end:], nil
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
