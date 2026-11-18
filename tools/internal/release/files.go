// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	beginInventory    = "<!-- BEGIN IMAGE INVENTORY -->"
	endInventory      = "<!-- END IMAGE INVENTORY -->"
	beginDependencies = "<!-- BEGIN CHART DEPENDENCIES -->"
	endDependencies   = "<!-- END CHART DEPENDENCIES -->"
)

// Change is a prepared documentation update. All inputs are read before writing.
type Change struct {
	Path     string
	Data     []byte
	mode     os.FileMode
	original []byte
}

// updateDependencyVersions preserves the table's descriptions and settings while
// reading direct chart versions from Chart.yaml, independently of image tags.
func updateDependencyVersions(root, original string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "nktp/Chart.yaml"))
	if err != nil {
		return "", err
	}
	var chart struct {
		Dependencies []struct{ Name, Version, Repository string }
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return "", err
	}
	versions := make(map[string]string)
	for _, dependency := range chart.Dependencies {
		if dependency.Name == "" || dependency.Version == "" || versions[dependency.Name] != "" {
			return "", fmt.Errorf("dependency names and versions must be nonempty and names unique in nktp/Chart.yaml")
		}
		versions[dependency.Name] = dependency.Version
	}
	start, end := strings.Index(original, beginDependencies), strings.Index(original, endDependencies)
	if strings.Count(original, beginDependencies) != 1 || strings.Count(original, endDependencies) != 1 || end < start {
		return "", fmt.Errorf("expected one ordered chart dependency marker pair in nktp/README.md")
	}
	start += len(beginDependencies)
	lines := strings.Split(original[start:end], "\n")
	seen := make(map[string]bool)
	for index, line := range lines {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 7 {
			return "", fmt.Errorf("expected five columns in chart dependency row: %s", line)
		}
		name := strings.TrimSpace(fields[1])
		if name == "Chart" || strings.Trim(name, "- :") == "" {
			continue
		}
		version := versions[name]
		if version == "" {
			return "", fmt.Errorf("no dependency version for %s in nktp/Chart.yaml", name)
		}
		if seen[name] {
			return "", fmt.Errorf("duplicate dependency row for %s", name)
		}
		seen[name] = true
		fields[2] = " " + version + " "
		lines[index] = strings.Join(fields, "|")
	}
	for _, dependency := range chart.Dependencies {
		// Local library charts do not deploy components of their own.
		if !strings.HasPrefix(dependency.Repository, "file://") && !seen[dependency.Name] {
			return "", fmt.Errorf("missing dependency row for %s in nktp/README.md", dependency.Name)
		}
	}
	return original[:start] + strings.Join(lines, "\n") + original[end:], nil
}

func replaceInventory(original, table string) (string, error) {
	if strings.Count(original, beginInventory) != 1 || strings.Count(original, endInventory) != 1 {
		return "", fmt.Errorf("expected exactly one image inventory marker pair in nktp/README.md")
	}
	start, end := strings.Index(original, beginInventory), strings.Index(original, endInventory)
	if end < start {
		return "", fmt.Errorf("image inventory markers are reversed")
	}
	return original[:start+len(beginInventory)] + "\n\n" + strings.TrimSpace(table) + "\n\n" + original[end:], nil
}

// Plan prepares image inventory and versioned Operator documentation updates.
// When from and to are provided, it also updates maintained chart references.
// Chart versions and release examples are not edited.
func Plan(root, from, to string, images ResolvedImages) ([]Change, error) {
	if from != "" || to != "" {
		if err := validateVersions(from, to); err != nil {
			return nil, err
		}
	}
	operatorVersion, err := images.operatorVersion()
	if err != nil {
		return nil, err
	}
	paths := []string{"README.md", "nktp/README.md"}
	err = filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relative)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	var changes []Change
	for _, path := range paths {
		absolute := filepath.Join(root, path)
		data, err := os.ReadFile(absolute)
		if err != nil {
			return nil, err
		}
		updated := string(data)
		if from != "" && (path == "README.md" || path == "nktp/README.md" || path == "docs/guides/use_blueprints.md") {
			updated = replaceVersion(updated, from, to)
		}
		updated = replaceOperatorReferences(updated, operatorVersion)
		if path == "nktp/README.md" {
			updated, err = updateDependencyVersions(root, updated)
			if err != nil {
				return nil, err
			}
			updated, err = replaceInventory(updated, images.Table)
			if err != nil {
				return nil, err
			}
		}
		if updated == string(data) {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		changes = append(changes, Change{Path: absolute, Data: []byte(updated), mode: info.Mode().Perm(), original: data})
	}
	return changes, nil
}

// Apply writes prepared changes, or reports stale files without writing in check mode.
func Apply(changes []Change, check bool) error {
	// Recheck every input before writing any output, so an edit made after Plan
	// cannot be silently overwritten.
	for _, change := range changes {
		current, err := os.ReadFile(change.Path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, change.original) {
			return fmt.Errorf("%s changed during generation; rerun the command", change.Path)
		}
	}
	if check && len(changes) > 0 {
		var paths []string
		for _, change := range changes {
			paths = append(paths, change.Path)
		}
		return fmt.Errorf("documentation needs updating: %s", strings.Join(paths, ", "))
	}
	for _, change := range changes {
		if err := os.WriteFile(change.Path, change.Data, change.mode); err != nil {
			return err
		}
	}
	return nil
}
