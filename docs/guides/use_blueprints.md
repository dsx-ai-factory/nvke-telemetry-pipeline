# Use blueprints

Every NKTP Collector configuration can use [NVCB](../../nvcb/README.md)
blueprints. NKTP expands them into native Collector
configuration when you install or upgrade the chart.

## Use a blueprint

NKTP's default values include its own blueprints, under `blueprints`. Each has a
`description` that lists its parameters. To use one, declare it with its
parameters and add it to a pipeline. This example sends a copy of the logs
gateway's output to another backend:

```yaml
gateway_logs:
  otelcol:
    config:
      exporters:
        blueprint/otlp_backend:
          endpoint: logs.example.com:4317
          signal: logs
      service:
        pipelines:
          logs:
            exporters: [otlp, blueprint/otlp_backend]
```

Helm replaces lists instead of merging them, so keep the pipeline's existing
exporters, `otlp` here.

## Add your own blueprints

Pass your blueprint files to NKTP as values files, before your main values file.
The [NVCB README](../../nvcb/README.md#bring-your-own-blueprints) lists the ways
to do that for each deployment tool.

Every Collector can use every blueprint under `blueprints`, NKTP's and yours. A
blueprint you pass replaces NKTP's blueprint of the same kind and type. A
definition in a Collector's own `otelcol.config.blueprints` takes precedence for
that Collector.

## Check the result

Render the chart and look at the Collector's configuration. Run this from the
repository root, and edit the values file path first:

```bash
helm template nktp ./nktp --namespace nktp \
  --values my-values.yaml \
  --show-only templates/gateway-logs.yaml
```

Each blueprint a pipeline uses becomes native components named after it. These
names also appear in the Collector's own metrics and logs:

```text
connectors:  forward/exp/otlp_backend
exporters:   otlp/(exp/otlp_backend)
pipelines:   logs/(exp/otlp_backend)
```
