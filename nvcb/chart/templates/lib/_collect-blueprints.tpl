{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* collectBlueprints collects declared and referenced blueprints along with user-defined values, scope and its template */}}
{{- define "nvcb.lib.collectBlueprints" -}}
    {{- $config := . -}}
    {{- $definitions := get $config "blueprints" | default dict -}}
    {{- $blueprints := dict "receivers" (dict) "exporters" (dict) -}}
    {{- $service := get $config "service" | default dict -}}
    {{- range $_, $pipeline := (get $service "pipelines" | default dict) -}}
        {{- range $kind := list "receivers" "exporters" -}}
            {{- $entries := get $blueprints $kind -}}
            {{- range $id := (get $pipeline $kind | default list) -}}
                {{- if or (not (hasPrefix "blueprint/" $id)) (hasKey $entries $id) -}}
                    {{- continue -}}
                {{- end -}}
                {{- $spec := trimPrefix "blueprint/" $id -}}
                {{- $blueprintType := first (splitList "/" $spec) -}}
                {{- $definition := get (get $definitions $kind) $blueprintType -}}
                {{- $values := get (get $config $kind) $id | default dict -}}
                {{- $scope := printf "%s/%s" (ternary "exp" "rcv" (eq $kind "exporters")) $spec -}}
                {{- $_ := set $entries $id (dict
                    "id" $id
                    "kind" $kind
                    "scope" (printf "(%s)" $scope)
                    "forward" (printf "forward/%s" $scope)
                    "template" $definition.template
                    "values" (deepCopy $values)
                ) -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}

    {{- $_ := unset $config "blueprints" -}}
    {{- range $kind := list "receivers" "exporters" -}}
        {{- $components := get $config $kind | default dict -}}
        {{- range $id, $_ := $components -}}
            {{- if hasPrefix "blueprint/" $id -}}
                {{- $_ := unset $components $id -}}
            {{- end -}}
        {{- end -}}
    {{- end -}}
    {{- toYaml $blueprints -}}
{{- end -}}
