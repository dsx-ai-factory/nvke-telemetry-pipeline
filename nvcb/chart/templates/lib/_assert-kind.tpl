{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/* assertKind validate a value's kind without defaulting or modifying it */}}
{{- define "nvcb.lib.assertKind" -}}
    {{- if not (kindIs .kind .value) -}}
        {{- fail .message -}}
    {{- end -}}
{{- end -}}
