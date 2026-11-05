# Blueprints Introduction

Blueprints are templated OpenTelemetry collector config fragments. Defining a config fragment as a blueprint makes it easily distributable and points back to a single source of truth.

See [blueprint guide](./create-a-blueprint.md) to create your own blueprint.

## Structure

It has four main sections:

1. Kind: blueprints must be either `receiver` or `exporter`.
2. Identifier: uses the format `blueprint/<type>[/<name>]`. Just like native components, you can name an exporter and receiver blueprint with the same type.
3. Template: section that contains the templated config fragment.
4. Parameters: values passed to your blueprint when an instance is declared. Only required parameters must be provided; the template can supply defaults for optional ones.

This minimal example shows the definition, template, and instance together. Instance parameters are available inside the template as `.values`; `endpoint` is required, while `interval` defaults to `30s`.

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
        pipelines:
          metrics:
            receivers: [httpcheck]
            exporters: [{{ .forward }}]

receivers:
  blueprint/http_health/orders:
    endpoint: http://127.0.0.1:8080/health

exporters:
  debug: {}

service:
  pipelines:
    metrics:
      receivers: [blueprint/http_health/orders]
      exporters: [debug]
```

## Generated forward connector

NVCB generates a forward connector that connects your blueprint's internal pipelines to the config pipelines using it. The connector's name is available in the blueprint's template context as `{{ .forward }}`.

The direction depends on the kind of blueprint:

| Kind | Inside the blueprint | In the consuming config |
| --- | --- | --- |
| Receiver | Must use `{{ .forward }}` in at least one pipeline's `exporters` list | References the blueprint in `receivers` |
| Exporter | Must use `{{ .forward }}` in at least one pipeline's `receivers` list | References the blueprint in `exporters` |

### Constraints

1. At least one internal pipeline must connect to `{{ .forward }}`. Other internal pipelines can connect through your own connectors.
2. Use the direction shown above; `{{ .forward }}` cannot be used as a processor.
3. Do not declare the generated connector yourself or hardcode its identifier; NVCB owns it.
4. Match the signals on both sides of the boundary. A `logs` pipeline cannot forward into a `metrics` pipeline; the boundary does not convert signals.

### Multiple signals

You can configure your blueprint to send to/receive from multiple signals. For instance, this exporter blueprint accepts both metrics and logs through the same `{{ .forward }}` connector:

```yaml
blueprints:
  exporters:
    debug_signals:
      template: |
        exporters:
          debug:
            verbosity: detailed
        pipelines:
          metrics:
            receivers: [{{ .forward }}]
            exporters: [debug]
          logs:
            receivers: [{{ .forward }}]
            exporters: [debug]
```

The config using it must connect those two signals:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317

exporters:
  blueprint/debug_signals: {}

service:
  pipelines:
    metrics:
      receivers: [otlp]
      exporters: [blueprint/debug_signals]
    logs:
      receivers: [otlp]
      exporters: [blueprint/debug_signals]
```

Both references resolve to `forward/exp/debug_signals`. Metrics go to the internal metrics pipeline, and logs go to the internal logs pipeline.

## Receiver Blueprints

Receiver blueprints package collection and processing behind a receiver reference. Your blueprint's pipelines prepare telemetry before passing it to the consuming config through the [connector boundary](#connector-boundary).

### Constraints

1. Can declare `receivers`, `processors`, `connectors`, and `extensions`, but **not** `exporters`.
2. At least one pipeline must use `{{ .forward }}` in its `exporters`, never its `receivers` or `processors`.
3. Define pipelines under `pipelines`, not `service.pipelines`; the template cannot define `service` settings.
4. Cannot declare or reference other blueprints inside the template.
5. Cannot declare the `{{ .forward }}` connector yourself; NVCB creates it.

**Receiver Blueprint Example**

This `json_app_logs` receiver blueprint contains a `filelog` receiver and `resource` processor. Its role is to process json logs in a particular way and add `service.name` to the resource attributes. In this case, `.values.path` and `.values.service_name` are the *required parameters* for this blueprint.

```yaml
blueprints:
  receivers:
    json_app_logs:
      template: |
        receivers:
          filelog:
            include:
              - {{ .values.path | required "json_app_logs: path is required" | quote }}
            start_at: end
            operators:
              - type: json_parser
                timestamp:
                  parse_from: attributes.timestamp
                  layout_type: gotime
                  layout: '2006-01-02T15:04:05Z07:00'
                severity:
                  parse_from: attributes.level
              - type: move
                from: attributes.message
                to: body

        processors:
          resource:
            attributes:
              - key: service.name
                value: {{ .values.service_name | required "json_app_logs: service_name is required" | quote }}
                action: upsert

        pipelines:
          logs:
            receivers: [filelog]
            processors: [resource]
            exporters: [{{ .forward }}]

receivers:
  blueprint/json_app_logs/orders:
    path: /var/log/orders/*.log
    service_name: orders
  blueprint/json_app_logs/payments:
    path: /var/log/payments/*.log
    service_name: payments

exporters:
  debug:
    verbosity: detailed

service:
  pipelines:
    logs:
      receivers:
        - blueprint/json_app_logs/orders
        - blueprint/json_app_logs/payments
      exporters: [debug]
```

Just like native components, you can define the same blueprint multiple times with different inputs like the example does with `blueprint/json_app_logs/{orders,payments}`.

## Exporter blueprints

Exporter blueprints are similar to receiver blueprints, but cannot have `receiver` components declared.

### Constraints

1. Can declare `exporters`, `processors`, `connectors`, and `extensions`, but **not** `receivers`.
2. At least one pipeline must use `{{ .forward }}` in its `receivers`, never its `exporters` or `processors`.
3. Define pipelines under `pipelines`, not `service.pipelines`; the template cannot define `service` settings.
4. Cannot declare or reference other blueprints inside the template.
5. Cannot declare the `{{ .forward }}` connector yourself; NVCB creates it.

**Exporter Blueprint Example**

```yaml
blueprints:
  exporters:
    otlp_logs:
      template: |
        processors:
          batch:
            send_batch_size: 1024
            timeout: 5s

        exporters:
          otlphttp:
            endpoint: {{ .values.endpoint | required "otlp_logs: endpoint is required" | quote }}

        pipelines:
          logs:
            receivers: [{{ .forward }}]
            processors: [batch]
            exporters: [otlphttp]

receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317

exporters:
  blueprint/otlp_logs/primary:
    endpoint: https://telemetry.example.com:4318

service:
  pipelines:
    logs:
      receivers: [otlp]
      exporters: [blueprint/otlp_logs/primary]
```

