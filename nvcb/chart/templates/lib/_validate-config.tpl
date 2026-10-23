{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* Collect input errors without modifying the config, then report them together. */}}
{{- define "nvcb.lib.validateConfig" -}}
    {{- $errors := list -}}
    {{- $config := . -}}
    {{- if kindIs "invalid" $config -}}
        {{- $config = dict -}}
    {{- end -}}
    {{- if not (kindIs "map" $config) -}}
        {{- $errors = append $errors "nvcb: config must be a map" -}}
    {{- else -}}
        {{- range $validator := list
            "nvcb.lib.validateConfig.catalog"
            "nvcb.lib.validateConfig.declarations"
            "nvcb.lib.validateConfig.service"
        -}}
            {{- $additional := include $validator (dict "config" $config) | fromYamlArray -}}
            {{- $errors = concat $errors $additional -}}
        {{- end -}}
    {{- end -}}
    {{- if eq (len $errors) 1 -}}
        {{- fail (first $errors) -}}
    {{- else if $errors -}}
        {{- fail (printf "nvcb: invalid configuration:\n- %s" (join "\n- " $errors)) -}}
    {{- end -}}
{{- end -}}

{{/* Validate catalog maps and template strings; return an error list as YAML. */}}
{{- define "nvcb.lib.validateConfig.catalog" -}}
    {{- $errors := list -}}
    {{- $definitions := .config.blueprints -}}
    {{- if kindIs "invalid" $definitions -}}
        {{- $definitions = dict -}}
    {{- end -}}
    {{- if not (kindIs "map" $definitions) -}}
        {{- $errors = append $errors "nvcb: blueprints must be a map" -}}
    {{- else -}}
        {{- range $kind := list "receivers" "exporters" -}}
            {{- if not (hasKey $definitions $kind) -}}
                {{- continue -}}
            {{- end -}}
            {{- $blueprintKindMap := get $definitions $kind -}}
            {{- if not (kindIs "map" $blueprintKindMap) -}}
                {{- $errors = append $errors (printf "nvcb: blueprints.%s must be a map" $kind) -}}
                {{- continue -}}
            {{- end -}}
            {{- range $blueprintType, $blueprint := $blueprintKindMap -}}
                {{- $id := printf "blueprint/%s" $blueprintType -}}
                {{- if not (kindIs "map" $blueprint) -}}
                    {{- $errors = append $errors (printf "nvcb: blueprint %q must be a map" $id) -}}
                {{- else if not (kindIs "string" $blueprint.template) -}}
                    {{- $errors = append $errors (printf "nvcb: blueprint %q must provide a template string" $id) -}}
                {{- end -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
    {{- toYaml $errors -}}
{{- end -}}

{{/* Validate component maps and declarations, including unused definitions. */}}
{{- define "nvcb.lib.validateConfig.declarations" -}}
    {{- $errors := list -}}
    {{- $config := .config -}}
    {{- $definitions := $config.blueprints -}}
    {{- if kindIs "invalid" $definitions -}}
        {{- $definitions = dict -}}
    {{- end -}}
    {{- range $kind := list "receivers" "processors" "exporters" "connectors" "extensions" -}}
        {{- if not (hasKey $config $kind) -}}
            {{- continue -}}
        {{- end -}}
        {{- $components := get $config $kind -}}
        {{- if not (kindIs "map" $components) -}}
            {{- $errors = append $errors (printf "nvcb: %s must be a map" $kind) -}}
            {{- continue -}}
        {{- end -}}
        {{- range $id, $parameters := $components -}}
            {{- if not (hasPrefix "blueprint/" $id) -}}
                {{- continue -}}
            {{- end -}}
            {{- if not (or (kindIs "invalid" $parameters) (kindIs "map" $parameters)) -}}
                {{- $errors = append $errors (printf "nvcb: blueprint declaration %q parameters must be a map" $id) -}}
            {{- end -}}
            {{- if not (has $kind (list "receivers" "exporters")) -}}
                {{- $errors = append $errors (printf "nvcb: %s blueprint declaration %q is not supported" (trimSuffix "s" $kind) $id) -}}
                {{- continue -}}
            {{- end -}}
            {{- $spec := trimPrefix "blueprint/" $id -}}
            {{- $parts := splitn "/" 2 $spec -}}
            {{- if or (eq $parts._0 "") (and (gt (len $parts) 1) (eq $parts._1 "")) -}}
                {{- $errors = append $errors (printf "nvcb: invalid blueprint declaration %q: expected blueprint/<type>[/<name>]" $id) -}}
                {{- continue -}}
            {{- end -}}
            {{- if not (kindIs "map" $definitions) -}}
                {{- continue -}}
            {{- end -}}
            {{- $blueprintKindMap := dict -}}
            {{- if hasKey $definitions $kind -}}
                {{- $blueprintKindMap = get $definitions $kind -}}
            {{- end -}}
            {{- if kindIs "map" $blueprintKindMap -}}
                {{- if not (hasKey $blueprintKindMap $parts._0) -}}
                    {{- $errors = append $errors (printf "nvcb: unknown blueprint type %q for %s declaration %q" $parts._0 (trimSuffix "s" $kind) $id) -}}
                {{- end -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
    {{- toYaml $errors -}}
{{- end -}}

{{/* Validate service structure and extensions, then delegate each pipeline. */}}
{{- define "nvcb.lib.validateConfig.service" -}}
    {{- $errors := list -}}
    {{- $config := .config -}}
    {{- $service := dict -}}
    {{- if hasKey $config "service" -}}
        {{- $service = get $config "service" -}}
    {{- end -}}
    {{- if not (kindIs "map" $service) -}}
        {{- $errors = append $errors "nvcb: service must be a map" -}}
    {{- else -}}
        {{- if hasKey $service "extensions" -}}
            {{- $extensions := get $service "extensions" -}}
            {{- if not (kindIs "slice" $extensions) -}}
                {{- $errors = append $errors "nvcb: service.extensions must be a list" -}}
            {{- else -}}
                {{- range $reference := $extensions -}}
                    {{- if not (kindIs "string" $reference) -}}
                        {{- $errors = append $errors "nvcb: service.extensions entries must be strings" -}}
                    {{- else if hasPrefix "blueprint/" $reference -}}
                        {{- $errors = append $errors (printf "nvcb: service.extensions references blueprint %q" $reference) -}}
                    {{- end -}}
                {{- end -}}
            {{- end -}}
        {{- end -}}
        {{- $pipelines := dict -}}
        {{- if hasKey $service "pipelines" -}}
            {{- $pipelines = get $service "pipelines" -}}
        {{- end -}}
        {{- if not (kindIs "map" $pipelines) -}}
            {{- $errors = append $errors "nvcb: service.pipelines must be a map" -}}
        {{- else -}}
            {{- range $pipelineID, $pipeline := $pipelines -}}
                {{- $additional := include "nvcb.lib.validateConfig.pipeline" (dict
                    "config" $config "id" $pipelineID "pipeline" $pipeline
                ) | fromYamlArray -}}
                {{- $errors = concat $errors $additional -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
    {{- toYaml $errors -}}
{{- end -}}

{{/* Validate one pipeline's shape, reference lists, and declaration lookups. */}}
{{- define "nvcb.lib.validateConfig.pipeline" -}}
    {{- $errors := list -}}
    {{- $config := .config -}}
    {{- $pipelineID := .id -}}
    {{- $pipeline := .pipeline -}}
    {{- if not (kindIs "map" $pipeline) -}}
        {{- $errors = append $errors (printf "nvcb: pipeline %q must be a map" $pipelineID) -}}
    {{- else -}}
        {{- range $kind := list "receivers" "processors" "exporters" -}}
            {{- if not (hasKey $pipeline $kind) -}}
                {{- continue -}}
            {{- end -}}
            {{- $references := get $pipeline $kind -}}
            {{- if not (kindIs "slice" $references) -}}
                {{- $errors = append $errors (printf "nvcb: pipeline %q %s must be a list" $pipelineID $kind) -}}
                {{- continue -}}
            {{- end -}}
            {{- range $reference := $references -}}
                {{- if not (kindIs "string" $reference) -}}
                    {{- $errors = append $errors (printf "nvcb: pipeline %q %s entries must be strings" $pipelineID $kind) -}}
                    {{- continue -}}
                {{- end -}}
                {{- if not (hasPrefix "blueprint/" $reference) -}}
                    {{- continue -}}
                {{- end -}}
                {{- if eq $kind "processors" -}}
                    {{- $errors = append $errors (printf "nvcb: pipeline %q references blueprint %q" $pipelineID $reference) -}}
                    {{- continue -}}
                {{- end -}}
                {{- $components := dict -}}
                {{- if hasKey $config $kind -}}
                    {{- $components = get $config $kind -}}
                {{- end -}}
                {{- if kindIs "map" $components -}}
                    {{- if not (hasKey $components $reference) -}}
                        {{- $errors = append $errors (printf "nvcb: pipeline %q references undeclared %s blueprint %q" $pipelineID (trimSuffix "s" $kind) $reference) -}}
                    {{- end -}}
                {{- end -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
    {{- toYaml $errors -}}
{{- end -}}
