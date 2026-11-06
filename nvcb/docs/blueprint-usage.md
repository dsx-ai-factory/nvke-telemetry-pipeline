# Using a blueprint

Use this guide to add existing receiver or exporter blueprints to your Collector config. You supply parameters and reference the blueprint in your pipelines.

1. Read the blueprint's description to learn which parameters it requires and accepts.
2. Declare the blueprint under the correct kind map in your collector config, supplying desired parameters and optionally adding a suffix to the declaration:

```yaml
receivers:
  blueprint/http_health/orders:
    endpoint: http://orders.default.svc.cluster.local:8080/health
    service_name: orders
```

3. Reference the blueprint in the pipeline signals that the blueprint requires under the correct kind:

```yaml
service:
  pipelines:
    metrics:
      receivers:
        - blueprint/http_health/orders
      exporters: [debug]
```

For complete examples, see [HTTP health receiver](./examples/http-health.yaml) and [authenticated OTLP exporter](./examples/authenticated-otlp.yaml).
