# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Validate and normalize known dependency versions for the default snapshot."""

import re
import sys


IMAGE_PREFIXES = (
    "otel/opentelemetry-collector-k8s:",
    "ghcr.io/open-telemetry/opentelemetry-operator/target-allocator:",
    "registry.k8s.io/kube-state-metrics/kube-state-metrics:v",
    "grafana/otel-lgtm:",
)
CHART_PREFIX = "opentelemetry-kube-stack-"
VERSION = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+")
FIELD = re.compile(r"^([ \t]*(?:-[ \t]+)?)(image|helm\.sh/chart):([ \t]*)([^\r\n]+)$", re.MULTILINE)


def normalize_field(match):
    indent, key, spacing, value = match.groups()
    quote = ""
    if value.startswith(("'", '"')) and value[-1] == value[0]:
        quote, value = value[0], value[1:-1]
    prefixes = (CHART_PREFIX,) if key == "helm.sh/chart" else IMAGE_PREFIXES
    for prefix in prefixes:
        if value.startswith(prefix):
            if not VERSION.fullmatch(value[len(prefix):]):
                raise ValueError(f"invalid dependency version in {key}: {value}")
            return f"{indent}{key}:{spacing}{quote}{prefix}VERSION{quote}"
    return match.group(0)


if __name__ == "__main__":
    try:
        # Preserve every other byte, including Helm's source comments used to
        # associate post-rendered documents with their original templates.
        sys.stdout.write(FIELD.sub(normalize_field, sys.stdin.read()))
    except ValueError as error:
        sys.exit(str(error))
