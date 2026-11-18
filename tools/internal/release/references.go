// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"fmt"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

var (
	operatorURLPattern  = regexp.MustCompile(`(https://(?:github\.com/open-telemetry/opentelemetry-operator/(?:blob|tree|releases/tag)/|raw\.githubusercontent\.com/open-telemetry/opentelemetry-operator/))(v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)([/#?)> \t\r\n]|$)`)
	markdownLinkPattern = regexp.MustCompile(`\[([^\]\n]*)\]\((https://[^)\s]+)\)`)
)

// replaceOperatorReferences updates versioned Operator links and matching
// versions in their labels. Branch and commit links retain their original refs.
func replaceOperatorReferences(original, version string) string {
	updated := markdownLinkPattern.ReplaceAllStringFunc(original, func(link string) string {
		parts := markdownLinkPattern.FindStringSubmatch(link)
		ref := operatorURLPattern.FindStringSubmatch(parts[2])
		if ref == nil {
			return link
		}
		label := replaceVersion(parts[1], strings.TrimPrefix(ref[2], "v"), version)
		return "[" + label + "](" + parts[2] + ")"
	})
	return operatorURLPattern.ReplaceAllString(updated, "${1}v"+version+"${3}")
}

func validateVersions(from, to string) error {
	if !versionPattern.MatchString(from) || !versionPattern.MatchString(to) || from == to {
		return fmt.Errorf("from and to must be different semantic versions")
	}
	return nil
}

func versionCharacter(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c == '.'
}

// replaceVersion preserves v prefixes and RC suffixes while avoiding substrings
// of longer versions or identifiers. Only maintained chart documents call it.
func replaceVersion(original, from, to string) string {
	pattern := regexp.MustCompile(`v?` + regexp.QuoteMeta(from))
	var output strings.Builder
	previous := 0
	for _, match := range pattern.FindAllStringIndex(original, -1) {
		start, end := match[0], match[1]
		continuesToken := end < len(original) && versionCharacter(original[end])
		// A final period is sentence punctuation, unless another version
		// character follows it (as in 0.2.0.1 or 0.2.0.example).
		if end < len(original) && original[end] == '.' && (end+1 == len(original) || !versionCharacter(original[end+1])) {
			continuesToken = false
		}
		if start > 0 && versionCharacter(original[start-1]) || continuesToken {
			continue
		}
		output.WriteString(original[previous:start])
		if original[start] == 'v' {
			output.WriteByte('v')
		}
		output.WriteString(to)
		previous = end
	}
	output.WriteString(original[previous:])
	return output.String()
}
