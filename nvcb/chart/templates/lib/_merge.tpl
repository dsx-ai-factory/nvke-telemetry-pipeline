{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* Rewrite user references and merge completed blueprints into the copied config. */}}
{{- define "nvcb.lib.merge" -}}
    {{- $config := .config -}}
    {{- $blueprint := .blueprint -}}
    {{- $fragment := $blueprint.fragment -}}
    {{- $service := get $config "service" | default dict -}}
    {{- $pipelines := get $service "pipelines" | default dict -}}
    {{- $extensions := get $service "extensions" | default list -}}

    {{- range $section := list "receivers" "processors" "exporters" "connectors" "extensions" -}}
        {{- $components := get $fragment $section | default dict -}}
        {{- if not $components -}}
            {{- continue -}}
        {{- end -}}
        {{- $target := get $config $section | default dict -}}
        {{- range $component, $settings := $components -}}
            {{- if hasKey $target $component -}}
                {{- if and (eq $section "connectors") (eq $component $blueprint.forward) -}}
                    {{- fail (printf "nvcb: blueprint %q conflicts with connector %q" $blueprint.id $component) -}}
                {{- end -}}
                {{- fail (printf "nvcb: blueprint %q conflicts with %s component %q" $blueprint.id $section $component) -}}
            {{- end -}}
            {{- $_ := set $target $component $settings -}}
            {{- if eq $section "extensions" -}}
                {{- $extensions = append $extensions $component -}}
            {{- end -}}
        {{- end -}}
        {{- $_ := set $config $section $target -}}
    {{- end -}}

    {{/* Rewrite only user pipelines, before adding already-scoped blueprint pipelines. */}}
    {{- range $pipelineID, $pipeline := $pipelines -}}
        {{- range $kind := list "receivers" "exporters" -}}
            {{- if hasKey $pipeline $kind -}}
                {{- $resolved := list -}}
                {{- range $reference := get $pipeline $kind -}}
                      {{- if and (eq $kind $blueprint.kind) (eq $reference $blueprint.id) -}}
                          {{- $reference = $blueprint.forward -}}
                      {{- end -}}
                    {{- $resolved = append $resolved $reference -}}
                {{- end -}}
                {{- $_ := set $pipeline $kind $resolved -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}

    {{- range $pipelineID, $pipeline := (get $fragment "pipelines" | default dict) -}}
        {{- if hasKey $pipelines $pipelineID -}}
            {{- fail (printf "nvcb: blueprint %q conflicts with pipeline %q" $blueprint.id $pipelineID) -}}
        {{- end -}}
        {{- $_ := set $pipelines $pipelineID $pipeline -}}
    {{- end -}}

    {{- $_ := set $service "pipelines" $pipelines -}}
    {{- $_ := set $config "service" $service -}}

    {{- if $extensions -}}
        {{- $_ := set $service "extensions" ($extensions | uniq | sortAlpha) -}}
    {{- end -}}
{{- end -}}
