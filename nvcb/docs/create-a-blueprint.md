# Creating a blueprint

## Blueprint definition and naming

Define a blueprint under a `blueprints` key and decide if your blueprint is a `receiver` or `exporter`. Define your blueprint identifier under that kind. Prefer using snake-case for the blueprint identifier.

```yaml
blueprints:
 receivers:
   http_health:
     ...
```

## Defining your blueprint's template

You must decide what your blueprint `template` will contain. Some important decisions to make are:

1. Which components will your blueprint use? *Refer back to the constraints for [receiver blueprints](./blueprints-intro.md#receiver-blueprints) or [exporter blueprints](./blueprints-intro.md#exporter-blueprints).*
2. Which signals will it emit to/receive from? *Refer back to [generated forward connector](./blueprints-intro.md#generated-forward-connector) for how this works.*
3. What are the required and optional parameters of your blueprint?

For example blueprints, check out [parsing access logs](./examples/nginx-access-logs.yaml) and [authenticated OTLP export](./examples/authenticated-otlp.yaml).

A blueprints `template` uses Go templating syntax, just like native Helm templates. All the same [template functions](https://helm.sh/docs/chart_template_guide/function_list/) apply to blueprints.

*In this example, the receiver blueprint performs endpoint monitoring. It expects `.values.endpoint` and `.values.service_name` as required parameters and `.values.interval` as an optional parameter with a `30s` default. The blueprint forwards to the `metrics` pipeline using the forward connector.*

```yaml
blueprints:
 receivers:
   http_health:
     template: |
       receivers:
         httpcheck:
           collection_interval: {{ .values.interval | default "30s" | quote }}
           targets:
             - endpoint: {{ .values.endpoint | required "http_health: endpoint is required" | quote }}
               method: GET
               timeout: 5s

       processors:
         resource:
           attributes:
             - key: service.name
               value: {{ .values.service_name | required "http_health: service_name is required" | quote }}
               action: upsert

       pipelines:
         metrics:
           receivers: [httpcheck]
           processors: [resource]
           exporters: [{{ .forward }}]
```

## Using your blueprint

In your OTel config, you instantiate the blueprint one or more times, optionally adding a named suffix to it. 

*In the example, the config **must** reference the blueprint in the `metrics` pipeline.*

```yaml
receivers:
 blueprint/http_health/orders:
   endpoint: http://orders.default.svc.cluster.local:8080/health
   service_name: orders
 blueprint/http_health/payments:
   endpoint: http://payments.default.svc.cluster.local:8080/health
   service_name: payments
   interval: 60s

exporters:
 debug:
   verbosity: detailed

service:
 pipelines:
   metrics:
     receivers:
       - blueprint/http_health/orders
       - blueprint/http_health/payments
     exporters: [debug]
```

## Passing lists and maps

Parameters can optionally contain nested lists and maps. For instance, this variation of `http_health` accepts several endpoints and a map of request headers:

```yaml
receivers:
  blueprint/http_health/orders:
    endpoints:
      - http://orders.default.svc.cluster.local:8080/health
      - http://orders.default.svc.cluster.local:8080/ready
    headers:
      Accept: application/json
      X-Probe: "true"
    service_name: orders
```

Replace the receiver block in the earlier template with this fragment; keep its resource processor and pipeline:

```gotemplate
receivers:
  httpcheck:
    collection_interval: {{ .values.interval | default "30s" | quote }}
    targets:
      - endpoints:
          {{- toYaml .values.endpoints | nindent 10 }}
        headers:
          {{- toYaml (.values.headers | default dict) | nindent 10 }}
```

Use `toYaml` to serialize lists and maps, then `nindent` to place them under the correct key. Use `quote` when emitting a scalar that must stay a string.

## Validating parameters

1. Use `required` to fail on a missing, null, or empty-string value. It does not validate the value's type or format.
2. Use `default` to supply a fallback, not to validate a value. It also replaces `false`, `0`, empty strings, empty lists, and empty maps; use `hasKey` when those are valid explicit inputs.
3. Use `kindIs` and `fail` for type checks and clear errors. Check types before applying defaults or serializing values.

For instance, add these checks at the start of the multi-endpoint template:

```gotemplate
{{- if not (kindIs "slice" .values.endpoints) -}}
  {{- fail "http_health: endpoints must be a list" -}}
{{- end -}}
{{- if eq (len .values.endpoints) 0 -}}
  {{- fail "http_health: endpoints must not be empty" -}}
{{- end -}}
{{- range .values.endpoints -}}
  {{- if not (kindIs "string" .) -}}
    {{- fail "http_health: each endpoint must be a string" -}}
  {{- end -}}
{{- end -}}
{{- if hasKey .values "headers" -}}
  {{- if not (kindIs "map" .values.headers) -}}
    {{- fail "http_health: headers must be a map" -}}
  {{- end -}}
{{- end -}}
```

See [Advanced Blueprints Topics](./blueprints-advanced.md) for component scoping and extension references.
