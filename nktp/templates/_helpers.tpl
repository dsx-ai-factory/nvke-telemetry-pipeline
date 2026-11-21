{{/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/}}
{{/*
Resolve an image map ({registry, repository, tag}) into an
OpenTelemetryCollector spec.image / container image string, honoring
global.imageRegistry.

When global.imageRegistry is set, it replaces the image's own registry.
The tag must be a string: YAML reads an unquoted 1.0 as a number.
*/}}
{{- define "nktp.image" -}}
{{- if not (kindIs "string" .image.tag) -}}
{{- fail (printf "image tag %v for %s must be a quoted string" .image.tag .image.repository) -}}
{{- end -}}
{{- $registry := .image.registry -}}
{{- if .imageRegistry -}}
{{- $registry = .imageRegistry -}}
{{- end -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry .image.repository .image.tag -}}
{{- else -}}
{{- printf "%s:%s" .image.repository .image.tag -}}
{{- end -}}
{{- end -}}

{{/*
Resolve targetallocator_image. An empty tag follows the Operator bundled by
opentelemetry-kube-stack, which releases the Target Allocator alongside it.
When the bundled Operator is disabled, its chart is not loaded; the
kube-stack appVersion names the same Operator release.
*/}}
{{- define "nktp.targetAllocatorImage" -}}
{{- $image := deepCopy .Values.targetallocator_image -}}
{{- if not $image.tag -}}
{{- $stack := index .Subcharts "opentelemetry-kube-stack" -}}
{{- $tag := $stack.Chart.AppVersion -}}
{{- with index $stack.Subcharts "opentelemetry-operator" -}}
{{- $tag = .Chart.AppVersion -}}
{{- end -}}
{{- $_ := set $image "tag" $tag -}}
{{- end -}}
{{- include "nktp.image" (dict "imageRegistry" .Values.global.imageRegistry "image" $image) -}}
{{- end -}}

{{/*
Return a Collector configuration as native Collector YAML, expanded by NVCB.

NVCB receives the configuration together with the definitions in the top-level
blueprints value. A definition in the configuration's own blueprints section
takes precedence. NVCB validates the definitions.

Arguments: config (the Collector configuration), blueprints (the top-level
blueprints value).
*/}}
{{- define "nktp.collectorConfig" -}}
{{- $config := deepCopy (.config | default dict) -}}
{{- $shared := .blueprints -}}
{{- if and $shared (kindIs "map" $config) -}}
  {{- $own := $config.blueprints | default dict -}}
  {{- if not (kindIs "map" $shared) -}}
    {{- $_ := set $config "blueprints" $shared -}}
  {{- else if kindIs "map" $own -}}
    {{- range $kind, $definitions := $shared -}}
      {{- $ownKind := get $own $kind -}}
      {{- if not (hasKey $own $kind) -}}
        {{- $_ := set $own $kind $definitions -}}
      {{- else if and (kindIs "map" $ownKind) (kindIs "map" $definitions) -}}
        {{- range $type, $definition := $definitions -}}
          {{- if not (hasKey $ownKind $type) -}}
            {{- $_ := set $ownKind $type $definition -}}
          {{- end -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
    {{- $_ := set $config "blueprints" $own -}}
  {{- end -}}
{{- end -}}
{{- include "nvcb.run" $config -}}
{{- end -}}

{{- define "nktp.builder.withTargetAllocator" -}}
  {{- $spec := .target -}}
  {{- if not (hasKey $spec "targetAllocator") -}}
    {{- $_ := set $spec "targetAllocator" (dict) -}}
  {{- end -}}
  {{- $allocator := $spec.targetAllocator -}}
  {{- if not (hasKey $allocator "image") -}}
    {{- $_ := set $allocator "image" (include "nktp.targetAllocatorImage" .root) -}}
  {{- end -}}
  {{- if not (hasKey $allocator "mtls") -}}
    {{- $_ := set $allocator "mtls" (dict) -}}
  {{- end -}}
  {{- if not (hasKey $allocator.mtls "useCertManager") -}}
    {{- $_ := set $allocator.mtls "useCertManager" .root.Values.certManager.targetAllocators.enabled -}}
  {{- end -}}
{{- end -}}

{{/* Unset priorityClassName inherits priorityClass.name; an empty string opts out. */}}
{{- define "nktp.builder.withPriorityClass" -}}
  {{- if not (hasKey .target "priorityClassName") -}}
    {{- $_ := set .target "priorityClassName" .root.Values.priorityClass.name -}}
  {{- end -}}
{{- end -}}

{{- define "nktp.builder.withImagePullSecrets" -}}
  {{- if and (not (hasKey .target "imagePullSecrets")) .root.Values.global.imagePullSecrets -}}
    {{- $_ := set .target "imagePullSecrets" (deepCopy .root.Values.global.imagePullSecrets) -}}
  {{- end -}}
{{- end -}}

{{- define "nktp.builder.withEnv" -}}
  {{- $env := .target.env | default list -}}
  {{- $item := .item -}}
  {{- $found := false -}}
  {{- range $env -}}
    {{- if eq .name $item.name -}}{{- $found = true -}}{{- end -}}
  {{- end -}}
  {{- if not $found -}}
    {{- $_ := set .target "env" (append $env $item) -}}
  {{- end -}}
{{- end -}}

{{/*
The Operator skips injection into Pods containing otc-container, so attach the
sidecar explicitly. An initContainer with restartPolicy: Always is a native
sidecar. See docs/architecture/self_telemetry.md.
*/}}
{{- define "nktp.builder.withSidecar" -}}
  {{- $spec := .target -}}
  {{- $otelcol := .sidecar.otelcol | default dict -}}
  {{- if not (kindIs "map" $otelcol.config) -}}
    {{- fail (printf "%s.sidecar.otelcol.config must be a native Collector YAML map" .name) -}}
  {{- end -}}
  {{- $image := include "nktp.image" (dict "imageRegistry" .root.Values.global.imageRegistry "image" ($otelcol.image | default .root.Values.otelcol_image)) -}}
  {{- $config := include "nktp.collectorConfig" (dict "config" $otelcol.config "blueprints" .root.Values.blueprints) | fromYaml | toJson -}}
  {{- $container := mustMergeOverwrite
    (deepCopy (.sidecar.kubernetes | default dict))
    (dict
      "name" "otc-sidecar"
      "image" $image
      "restartPolicy" "Always"
      "args" (list (printf "--config=yaml:%s" $config))
    )
  -}}
  {{- $podName := dict "name" "POD_NAME" "valueFrom" (dict "fieldRef" (dict "fieldPath" "metadata.name")) -}}
  {{- include "nktp.builder.withEnv" (dict "target" $container "item" $podName) -}}
  {{- $_ := set $spec "initContainers" (append ($spec.initContainers | default list) $container) -}}
{{- end -}}

{{/*
Return {scrapeClasses: <list>}: the Target Allocator's scrape classes, each with
a first relabeling that keeps (action keep) or drops (action drop) the targets
that have a node. When no class is the default, a default class named name is
added, so monitors without scrapeClassName are split too. Because every class
carries the split, a monitor that names its own class is still scraped by one
pool only.

The regex mirrors Item.GetNodeName in the pinned Target Allocator: a node label
is set, or the EndpointSlice address targets a Node by name. This is how the
per-node strategy places a target.

Arguments: classes (list, may be empty), action (keep or drop), name (default
class name).
*/}}
{{- define "nktp.nodeScrapeClasses" -}}
{{- $rule := dict "sourceLabels" (list "__meta_kubernetes_pod_node_name" "__meta_kubernetes_node_name" "__meta_kubernetes_endpoint_node_name" "__meta_kubernetes_endpointslice_address_target_kind" "__meta_kubernetes_endpointslice_address_target_name") "separator" ";" "regex" "[^;]+;.*|[^;]*;[^;]+;.*|[^;]*;[^;]*;[^;]+;.*|[^;]*;[^;]*;[^;]*;Node;[^;]+" "action" .action -}}
{{- $classes := list -}}
{{- $hasDefault := false -}}
{{- range $class := .classes | default list -}}
  {{- $class = deepCopy $class -}}
  {{- $_ := set $class "relabelings" (prepend ($class.relabelings | default list) $rule) -}}
  {{- if $class.default -}}{{- $hasDefault = true -}}{{- end -}}
  {{- $classes = append $classes $class -}}
{{- end -}}
{{- if not $hasDefault -}}
  {{- $classes = prepend $classes (dict "name" .name "default" true "relabelings" (list $rule)) -}}
{{- end -}}
{{- toYaml (dict "scrapeClasses" $classes) -}}
{{- end -}}

{{/*
Return {env: <list>} with K8S_CLUSTER_NAME set to global.clusterName, or an
empty list when it is not set. The transform/cluster processor in the gateways,
the watchdog and the watchdog sidecar reads this variable inside an OTTL string,
so the name must not contain a double quote or a backslash.
*/}}
{{- define "nktp.clusterNameEnv" -}}
{{- $env := list -}}
{{- $raw := .Values.global.clusterName -}}
{{- /* Only a missing or empty value is unset. 0 and false are names, not "unset". */}}
{{- $name := ternary "" (toString $raw) (kindIs "invalid" $raw) -}}
{{- if ne $name "" -}}
{{- if or (contains "\"" $name) (contains "\\" $name) -}}
{{- fail "global.clusterName must not contain double quotes or backslashes" -}}
{{- end -}}
{{- $env = list (dict "name" "K8S_CLUSTER_NAME" "value" $name) -}}
{{- end -}}
{{- toYaml (dict "env" $env) -}}
{{- end -}}

{{- define "nktp.tlsServers" -}}
{{- list
      (dict "name" "nktp-watchdog-server" "secretName" "nktp-watchdog-server-tls" "serviceName" "nktp-watchdog-collector")
      (dict "name" "nktp-gateway-logs-server" "secretName" "nktp-gateway-logs-server-tls" "serviceName" "nktp-gateway-logs-collector" "headless" true)
      (dict "name" "nktp-gateway-metrics-server" "secretName" "nktp-gateway-metrics-server-tls" "serviceName" "nktp-gateway-metrics-collector" "headless" true)
      (dict "name" "nktp-gateway-traces-server" "secretName" "nktp-gateway-traces-server-tls" "serviceName" "nktp-gateway-traces-collector" "headless" true) | toJson -}}
{{- end -}}

{{- define "nktp.tlsClients" -}}
{{- list
      (dict "name" "nktp-daemonset-client" "commonName" "nktp-daemonset")
      (dict "name" "nktp-large-target-metrics-client" "commonName" "nktp-large-target-metrics")
      (dict "name" "nktp-nodeless-metrics-client" "commonName" "nktp-nodeless-metrics")
      (dict "name" "nktp-cluster-events-client" "commonName" "nktp-cluster-events")
      (dict "name" "nktp-gateway-traces-client" "commonName" "nktp-gateway-traces")
      (dict "name" "nktp-gateway-logs-client" "commonName" "nktp-gateway-logs")
      (dict "name" "nktp-gateway-metrics-client" "commonName" "nktp-gateway-metrics") | toJson -}}
{{- end -}}

{{- define "nktp.taCertificates" -}}
{{- $names := list -}}
{{- if .Values.certManager.targetAllocators.enabled -}}
{{- /* The Operator creates these Certificates at runtime and names each one's Secret the same. */ -}}
{{- range $collector := list "nktp-daemonset" "nktp-large-target-metrics" "nktp-nodeless-metrics" "nktp-watchdog" -}}
{{- range $suffix := list "ca-cert" "ta-server-cert" "ta-client-cert" -}}
{{- $names = append $names (printf "%s-%s" $collector $suffix) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $names | toJson -}}
{{- end -}}

{{- define "nktp.certSecrets" -}}
{{- $secrets := list -}}
{{- if .Values.certManager.internalTLS.enabled -}}
{{- if empty .Values.certManager.internalTLS.issuerRef -}}
{{- $secrets = append $secrets "nktp-internal-ca" -}}
{{- end -}}
{{- range include "nktp.tlsServers" . | fromJsonArray -}}
{{- $secrets = append $secrets .secretName -}}
{{- end -}}
{{- range include "nktp.tlsClients" . | fromJsonArray -}}
{{- $secrets = append $secrets (printf "%s-tls" .name) -}}
{{- end -}}
{{- end -}}
{{- $secrets = concat $secrets (include "nktp.taCertificates" . | fromJsonArray) -}}
{{- /* The Operator subchart renders the webhook Certificate only while the Operator is enabled and admissionWebhooks.create and its certManager are on; the first two default to true upstream. */ -}}
{{- $operator := dig "opentelemetry-kube-stack" "opentelemetry-operator" (dict) .Values.AsMap -}}
{{- if and (dig "enabled" true $operator) (dig "admissionWebhooks" "create" true $operator) (dig "admissionWebhooks" "certManager" "enabled" false $operator) -}}
{{- $secrets = append $secrets (default "nktp-operator-controller-manager-service-cert" (dig "admissionWebhooks" "secretName" "" $operator)) -}}
{{- end -}}
{{- $secrets | toJson -}}
{{- end -}}
