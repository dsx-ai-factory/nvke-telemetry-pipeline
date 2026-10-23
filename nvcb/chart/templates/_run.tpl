{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
    run expands referenced blueprints in an OpenTelemetry Collector config
    and emits native Collector YAML without blueprint definitions or declarations.
*/}}
{{- define "nvcb.run" -}}
    {{- include "nvcb.lib.validateConfig" . -}}
    {{- $config := deepCopy (. | default dict) -}}

    {{- $blueprints := include "nvcb.lib.collectBlueprints" $config | fromYaml -}}

    {{- if or $blueprints.receivers $blueprints.exporters -}}
        {{- range $entries := $blueprints -}}
            {{- range $blueprint := $entries -}}
                {{- include "nvcb.lib.render" $blueprint -}}
                {{- include "nvcb.lib.validateBlueprint" $blueprint -}}
                {{- include "nvcb.lib.scope" $blueprint -}}
                {{- include "nvcb.lib.merge" (dict "config" $config "blueprint" $blueprint) -}}
            {{- end -}}
        {{- end -}}

        {{/* Finalize extensions once, after all blueprint additions. */}}
        {{- $service := get $config "service" | default dict -}}
        {{- $extensions := get $service "extensions" | default list -}}
        {{- if $extensions -}}
            {{- $_ := set $service "extensions" ($extensions | uniq | sortAlpha) -}}
        {{- end -}}
    {{- end -}}
    {{- toYaml $config -}}
{{- end -}}
