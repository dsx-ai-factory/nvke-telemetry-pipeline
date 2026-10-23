{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* render renders a template using the user-defined values and defaults and assigns it to the passed in blueprint object */}}
{{- define "nvcb.lib.render" -}}
    {{- $blueprint := . -}}
    {{- $context := dict "values" $blueprint.values "scope" $blueprint.scope "forward" $blueprint.forward -}}
    {{/* The named define/tpl wrapper adds the blueprint ID to execution errors;
         rebinding $ and dot restricts the body to values, scope, and forward. */}}
    {{- $bodyTemplate := printf "{{- $ := .context -}}{{- with $ -}}%s{{- end -}}" $blueprint.template -}}
    {{- $templateName := printf "nvcb: %s" $blueprint.id -}}
    {{- $template := printf "{{- define %q -}}{{- tpl .body . -}}{{- end -}}{{- template %q . -}}" $templateName $templateName -}}
    {{- $fragment := tpl $template (dict "body" $bodyTemplate "context" $context "Template" (dict "BasePath" "nvcb/templates" "Name" $blueprint.id)) | fromYaml -}}
    {{- $_ := set $blueprint "fragment" $fragment -}}
{{- end -}}
