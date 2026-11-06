// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command package-blueprints validates the blueprint files in blueprints/ and
// packages them as the default blueprints in nktp/values.yaml. With -check, it
// changes nothing and fails if nktp/values.yaml is out of date. The default
// paths are relative to the tools directory, where the blueprints tasks run.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
)

const (
	beginMarker = "# BEGIN DEFAULT BLUEPRINTS"
	endMarker   = "# END DEFAULT BLUEPRINTS"
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

func main() {
	dir := flag.String("blueprints", "../blueprints", "directory of blueprint files")
	file := flag.String("values", "../nktp/values.yaml", "values file to update")
	checkOnly := flag.Bool("check", false, "fail if the values file is out of date instead of updating it")
	flag.Parse()
	log.SetFlags(0)

	data, err := os.ReadFile(*file)
	if err != nil {
		log.Fatal(err)
	}
	values, err := packageValues(*dir, string(data))
	if err != nil {
		log.Fatal(err)
	}
	if *checkOnly {
		if values != string(data) {
			log.Fatal("nktp/values.yaml does not match blueprints/. Run `task blueprints` and commit the result.")
		}
		return
	}
	if err := os.WriteFile(*file, []byte(values), 0o644); err != nil {
		log.Fatal(err)
	}
}

// packageValues validates every blueprint file under dir and returns values,
// the contents of the chart's values file, with the default blueprints section
// replaced by their definitions.
func packageValues(dir, values string) (string, error) {
	section, err := defaultSection(dir)
	if err != nil {
		return "", err
	}
	return replaceSection(values, section)
}

// defaultSection returns the default blueprints section: one blueprints map
// that holds every definition under dir, sorted by kind and type.
func defaultSection(dir string) (string, error) {
	definitions := map[string]map[string]*yaml.Node{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".yaml" {
			return err
		}
		kind, name, definition, err := load(dir, path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if definitions[kind] == nil {
			definitions[kind] = map[string]*yaml.Node{}
		}
		definitions[kind][name] = definition
		return nil
	})
	if err != nil {
		return "", err
	}

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
	if err := encoder.Encode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("blueprints"), kinds}}); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// load reads a blueprint file, which must be <kind>/<type>.yaml under dir and
// define only blueprints.<kind>.<type>, and returns that definition.
func load(dir, path string) (kind, name string, definition *yaml.Node, err error) {
	relative, err := filepath.Rel(dir, path)
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
		return "", fmt.Errorf("the values file must contain one %q line followed by one %q line", beginMarker, endMarker)
	}
	start := begin + len(beginMarker) + 1
	return values[:start] + section + values[end:], nil
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
