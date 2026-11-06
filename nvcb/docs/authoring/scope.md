# Scope

Scope is a generated suffix that gives each blueprint instance’s components and pipelines distinct IDs in the rendered Collector config.

- For receiver blueprints, scope is `(rcv/<type>[/<name>])`.
- For exporter blueprints, scope is `(exp/<type>[/<name>])`.

NVCB automatically scopes:
- Component ID declarations e.g. `receivers.otlp`
- Pipeline ID declarations e.g. `pipelines.logs`
- Pipeline references e.g. `pipelines.logs.receivers: [otlp]`

**Before rendering**

```yaml
blueprints:
  exporters:
    otlp:
      template: |
        exporters:
          otlp:
            endpoint: 0.0.0.0:4318
        pipelines:
          metrics:
            receivers: [{{ .forward }}]
            exporters: [otlp]
receivers:
  nop: {}

exporters:
  blueprint/otlp: {}

service:
  pipelines:
    metrics:
      receivers: [nop]
      exporters: [blueprint/otlp]
```

**After rendering**

```yaml
receivers:
  nop: {}

exporters:
  otlp/(exp/otlp):          # Scoped component ID
    endpoint: 0.0.0.0:4318

connectors:
  forward/exp/otlp: {}      # Scoped generated forward connector

service:
  pipelines:
    metrics: 
      receivers: [nop]
      exporters: [forward/exp/otlp]
    metrics/(exp/otlp):             # Scoped pipeline ID
      receivers: [forward/exp/otlp]
      exporters: [otlp/(exp/otlp)]  # Scoped pipeline reference
```

For more examples of before-and-after rendering scenarios, see our [Test cases](../../tests/pass/).

## When to append scope
References to component or pipeline IDs inside a component's configuration are **not automatically scoped**. This is crucial in two common scenarios:

### 1. Extension references
In this example, the `filelog` receiver uses the `file_storage` extension to persist file offsets. Its `storage` field must reference the extension's complete scoped name:

```yaml
blueprints:
  receivers:
    file_log:
      template: |
        extensions:
          file_storage:
            directory: {{ .values.storage_directory | required "file_logs: storage_directory is required" | quote }}

        receivers:
          filelog:
            include:
              - {{ .values.path | required "file_logs: path is required" | quote }}
            start_at: end
            storage: file_storage/{{ .scope }}  # Need manual scoping here

        pipelines:
          logs:
            receivers: [filelog]
            exporters: [{{ .forward }}]
```

## 2. Routing connector references

The same rule applies to pipeline references inside a connector's config.

```yaml
blueprints:
  receivers:
    routed_logs:
      template: |
        receivers:
          otlp:
            protocols:
              grpc:
                endpoint: 127.0.0.1:4317

        connectors:
          routing:
            default_pipelines: [logs/default/{{ .scope }}]      # Need manual scoping 
            table:
              - context: log
                condition: 'attributes["route"] == "priority"'
                pipelines: [logs/priority/{{ .scope }}]         # Need manual scoping

        pipelines:
          logs/collect:
            receivers: [otlp]
            exporters: [routing]
          logs/default:
            receivers: [routing]
            exporters: [{{ .forward }}]
          logs/priority:
            receivers: [routing]
            exporters: [{{ .forward }}]
```

