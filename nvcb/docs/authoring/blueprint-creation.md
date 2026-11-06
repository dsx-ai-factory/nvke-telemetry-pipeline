# Creating a blueprint

Use this guide to create a new OpenTelemetry Collector blueprint. Check out the blueprint [examples](../examples/) for inspiration.

1. Create a blueprints key in the collector config you pass into `nvcb.run` or at the top level if your chart copies it in (see [README Option 2](../../README.md#usage)). Within that map, NVCB supports two blueprint kinds: `receivers` and `exporters`. To learn more about both blueprint kinds, see [Blueprint Kinds](./blueprint-kinds.md).

2. Next, choose your blueprint's type under your chosen kind (snake-case is encouraged for the naming pattern but not enforced).

```yaml
blueprints:
  receivers:
    http_health:
      ...
```

3. Create your blueprint `template`. The blueprint template adheres to Helm templating rules and contains the same [templating functions](https://helm.sh/docs/chart_template_guide/function_list/) that Helm exposes. Similar to a Helm chart, your blueprint template can require specific parameters of any shape (primitive, list, map, object) and you can use the available Helm template functions to validate and default those parameters.

> [!NOTE]
> If a component's config references another component or pipeline defined in the same blueprint, that reference needs an explicit scope. See [Scope](./scope.md#when-to-append-scope) for when and how to use `{{ .scope }}`.

*In this example, `.values.interval` is an optional parameter and `.values.endpoint` is a required parameter to this blueprint. Check out the full example [here](../examples/http-health.yaml)*

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
        ...
```

4. Decide which pipeline(s) your blueprint should forward to and mark the pipeline(s) with `{{ .forward }}`. The generated forward connector will fill in this value upon config expansion.

*In this continued example, the receiver blueprint sends out to the `metrics` pipeline, thus the `{{ .forward }}` connector set there.*

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

5. Add additional metadata to your blueprint to make it more easily digestible by consumers.

```yaml
blueprints:
  receivers:
    http_health:
      version: 0.1.0
      description: |
        Checks an HTTP endpoint and labels its metrics with service.name.
        Parameters:
          endpoint (required): the URL to check.
          service_name (required): the service.name resource attribute.
          interval (optional): the check interval, defaults to 30s.
    ...
```
