{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* 
  scope adds the unique prefix to blueprint component declarations, pipeline signals, 
  pipeline references as well as the generated forward connector. The new scoped template fragment
  is assigned to the passed in blueprint.
*/}}
{{- define "nvcb.lib.scope" -}}
    {{- $blueprint := . -}}
    {{- $body := deepCopy $blueprint.fragment -}}
    {{- $sections := list "receivers" "processors" "exporters" "connectors" "extensions" -}}
    {{- $scopeMap := dict -}}

    {{/* Map top-level component IDs to their scoped equivalents. */}}
    {{- range $section := $sections -}}
        {{- range $component, $_ := (get $body $section | default dict) -}}
            {{- $_ := set $scopeMap $component (printf "%s/%s" $component $blueprint.scope) -}}
        {{- end -}}
    {{- end -}}
    {{/* Map pipeline IDs to their scoped equivalents. */}}
    {{- range $pipelineID, $_ := (get $body "pipelines" | default dict) -}}
        {{- $_ := set $scopeMap $pipelineID (printf "%s/%s" $pipelineID $blueprint.scope) -}}
    {{- end -}}

    {{/* Rewrite pipeline references; external IDs and .forward stay unchanged. */}}
    {{- range $pipeline := (get $body "pipelines" | default dict) -}}
        {{- range $pipelineKind := list "receivers" "processors" "exporters" -}}
            {{- if hasKey $pipeline $pipelineKind -}}
                {{- $scopedRefs := list -}}
                {{- range $ref := get $pipeline $pipelineKind -}}
                    {{- if hasKey $scopeMap $ref -}}
                        {{- $ref = get $scopeMap $ref -}}
                    {{- end -}}
                    {{- $scopedRefs = append $scopedRefs $ref -}}
                {{- end -}}
                {{- $_ := set $pipeline $pipelineKind $scopedRefs -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}

    {{/* Rebuild each present section under scoped IDs, preserving empty sections. */}}
    {{- range $section := append $sections "pipelines" -}}
        {{- if hasKey $body $section -}}
            {{- $scopedSection := dict -}}
            {{- range $localID, $settings := get $body $section -}}
                {{- $_ := set $scopedSection (get $scopeMap $localID) $settings -}}
            {{- end -}}
            {{- $_ := set $body $section $scopedSection -}}
        {{- end -}}
    {{- end -}}

    {{/* Add the unparenthesized interface connector after scoping the blueprint's components. */}}
    {{- $connectors := get $body "connectors" | default dict -}}
    {{- $_ := set $connectors $blueprint.forward (dict) -}}
    {{- $_ := set $body "connectors" $connectors -}}

    {{- $_ := set $blueprint "fragment" $body -}}
{{- end -}}
