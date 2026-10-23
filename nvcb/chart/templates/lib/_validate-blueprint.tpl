{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* Validate blueprint fragment structure; signal compatibility is left to the Collector. */}}
{{- define "nvcb.lib.validateBlueprint" -}}
    {{- $id := .id -}}
    {{- $kind := .kind -}}
    {{- $body := .fragment -}}
    {{- $blueprint := . -}}
    {{- $bodyKeys := list "receivers" "processors" "exporters" "connectors" "extensions" "pipelines" -}}

    {{- if hasKey $body "Error" -}}
        {{- fail (printf "nvcb: blueprint %q emitted invalid YAML: %s" $id $body.Error) -}}
    {{- end -}}

    {{- range $section, $contents := $body -}}
        {{- if not (has $section $bodyKeys) -}}
            {{- fail (printf "nvcb: blueprint %q emitted unsupported top-level key %q" $id $section) -}}
        {{- end -}}
        {{- include "nvcb.lib.assertKind" (dict "value" $contents "kind" "map" "message" (printf "nvcb: blueprint %q section %q must be a map" $id $section)) -}}
    {{- end -}}

    {{- $forwardKind := ternary "receivers" "exporters" (eq $kind "exporters") -}}
    {{- range $component, $_ := (get $body $forwardKind | default dict) -}}
        {{- fail (printf "nvcb: %s blueprint %q declares %s %q" (trimSuffix "s" $kind) $id (trimSuffix "s" $forwardKind) $component) -}}
    {{- end -}}

    {{- range $pipelineID, $pipeline := (get $body "pipelines" | default dict) -}}
        {{- include "nvcb.lib.assertKind" (dict "value" $pipeline "kind" "map" "message" (printf "nvcb: blueprint %q pipeline %q must be a map" $id $pipelineID)) -}}
        {{- range $pipelineKind := list "receivers" "processors" "exporters" -}}
            {{- $references := list -}}
            {{- if hasKey $pipeline $pipelineKind -}}
                {{- $references = get $pipeline $pipelineKind -}}
                {{- include "nvcb.lib.assertKind" (dict "value" $references "kind" "slice" "message" (printf "nvcb: blueprint %q pipeline %q %s must be a list" $id $pipelineID $pipelineKind)) -}}
            {{- end -}}
            {{- range $reference := $references -}}
                {{- if hasPrefix "blueprint/" $reference -}}
                    {{- fail (printf "nvcb: blueprint %q cannot declare nested blueprint %q" $id $reference) -}}
                {{- end -}}
                {{- if eq $reference $blueprint.forward -}}
                    {{- if ne $pipelineKind $forwardKind -}}
                        {{- fail (printf "nvcb: blueprint %q pipeline %q must use .forward in %s" $id $pipelineID $forwardKind) -}}
                    {{- end -}}
                {{- end -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}

    {{- range $section := $bodyKeys -}}
        {{- range $component, $_ := (get $body $section | default dict) -}}
            {{- if eq $component $blueprint.forward -}}
                {{- fail (printf "nvcb: blueprint %q cannot declare reserved forward connector %q" $id $component) -}}
            {{- end -}}
            {{- if hasPrefix "blueprint/" $component -}}
                {{- fail (printf "nvcb: blueprint %q cannot declare nested blueprint %q" $id $component) -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
{{- end -}}
