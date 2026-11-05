# Advanced Blueprints Topics

Topics beyond basic blueprint authoring.

## Scope

Scope is the generated value used to namespace blueprint-defined components and pipelines in the rendered config.

- For receiver blueprints, scope is `(rcv/<type>[/<name>])`.
- For exporter blueprints, scope is `(exp/<type>[/<name>])`.

The scoped ID is `<id>/<scope>`. The value of `{{ .scope }}` includes the parentheses but **not** the leading slash, so write `file_storage/{{ .scope }}`, not `file_storage{{ .scope }}`.

Two instances of the same blueprint can both declare `otlp`; each gets a different scoped ID. A native `otlp` declared outside the blueprint keeps its name and can coexist with both. If a native component already uses a generated ID, like `otlp/(rcv/my_receiver)`, NVCB fails with a component conflict rather than overwriting it.

**Before rendering**

```yaml
blueprints:
  receivers:
    my_receiver:
      template: |
        receivers:
          otlp:
            protocols:
              grpc:
                endpoint: 0.0.0.0:4317
        pipelines:
          logs:
            receivers: [otlp]
            exporters: [{{ .forward }}]

receivers:
  blueprint/my_receiver: {}

exporters:
  debug: {}

service:
  pipelines:
    logs:
      receivers: [blueprint/my_receiver]
      exporters: [debug]
```

**After rendering**

```yaml
receivers:
  otlp/(rcv/my_receiver):
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

connectors:
  forward/rcv/my_receiver: {}

exporters:
  debug: {}

service:
  pipelines:
    logs/(rcv/my_receiver): # internal pipeline
      receivers: [otlp/(rcv/my_receiver)]
      exporters: [forward/rcv/my_receiver]
    logs:
      receivers: [forward/rcv/my_receiver]
      exporters: [debug]
```

#### Extension references

In this example, the `filelog` receiver uses the `file_storage` extension to persist file offsets. Its `storage` field must reference the extension's complete scoped name:

```yaml
blueprints:
  receivers:
    file_logs:
      template: |
        extensions:
          file_storage:
            directory: {{ .values.storage_directory | required "file_logs: storage_directory is required" | quote }}

        receivers:
          filelog:
            include:
              - {{ .values.path | required "file_logs: path is required" | quote }}
            start_at: end
            storage: file_storage/{{ .scope }}

        pipelines:
          logs:
            receivers: [filelog]
            exporters: [{{ .forward }}]

receivers:
  blueprint/file_logs/app:
    path: /var/log/app/*.log
    storage_directory: /var/lib/otelcol/app

exporters:
  debug:
    verbosity: detailed

service:
  pipelines:
    logs:
      receivers: [blueprint/file_logs/app]
      exporters: [debug]
```

Keep the extension declaration as `file_storage`, not `file_storage/{{ .scope }}`; NVCB adds the scope and enables the extension under `service.extensions`. Only the receiver's `storage` reference needs the explicit scope.

#### Routing connector references

The same rule applies to pipeline references inside a connector's config. See the [routing connector example](./examples/routed-logs.yaml) for the complete config.
