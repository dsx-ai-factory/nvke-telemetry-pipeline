{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* Rewrite user references and merge blueprint into the copied config. */}}
{{- define "nvcb.lib.merge" -}}
    {{- $config := .config -}}
    {{- $blueprint := .blueprint -}}
    {{- $service := get $config "service" | default dict -}}
    {{- $pipelines := get $service "pipelines" | default dict -}}
    {{- $extensions := get $service "extensions" | default list -}}

    {{- range $section := list "receivers" "processors" "exporters" "connectors" "extensions" -}}
        {{- $components := get $blueprint.fragment $section | default dict -}}
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

    {{/* Rewrite references for this blueprint's kind only. */}}
    {{- range $pipeline := $pipelines -}}
        {{- if hasKey $pipeline $blueprint.kind -}}
            {{- $resolved := list -}}
            {{- range $reference := get $pipeline $blueprint.kind -}}
                {{- if eq $reference $blueprint.id -}}
                    {{- $reference = $blueprint.forward -}}
                {{- end -}}
                {{- $resolved = append $resolved $reference -}}
            {{- end -}}
            {{- $_ := set $pipeline $blueprint.kind $resolved -}}
        {{- end -}}
    {{- end -}}

    {{- range $pipelineID, $pipeline := (get $blueprint.fragment "pipelines" | default dict) -}}
        {{- if hasKey $pipelines $pipelineID -}}
            {{- fail (printf "nvcb: blueprint %q conflicts with pipeline %q" $blueprint.id $pipelineID) -}}
        {{- end -}}
        {{- $_ := set $pipelines $pipelineID $pipeline -}}
    {{- end -}}

    {{- $_ := set $service "pipelines" $pipelines -}}
    {{- if $extensions -}}
        {{- $_ := set $service "extensions" $extensions -}}
    {{- end -}}
    {{- $_ := set $config "service" $service -}}
{{- end -}}
