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

    {{- if not (or $blueprints.receivers $blueprints.exporters) -}}
        {{- toYaml $config -}}
    {{- else -}}
        {{- range $entries := $blueprints -}}
            {{- range $blueprint := $entries -}}
                {{- include "nvcb.lib.render" $blueprint -}}
                {{- include "nvcb.lib.validateBlueprint" $blueprint -}}
                {{- include "nvcb.lib.scope" $blueprint -}}
            {{- end -}}
        {{- end -}}

        {{- include "nvcb.lib.merge" (dict "config" $config "blueprints" $blueprints) -}}
    {{- end -}}
{{- end -}}
