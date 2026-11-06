# Use blueprints

Every NKTP Collector configuration can use [NVCB](../../nvcb/README.md)
blueprints. NKTP expands them into native Collector configuration when Helm
renders the chart.

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
            exporters: [otlp_grpc, blueprint/otlp_backend]
```

Helm replaces lists instead of merging them, so keep the pipeline's existing
exporters, `otlp_grpc` here.

## Add your own blueprints

A blueprint file is a YAML file with this schema:

```yaml
blueprints:
  exporters: # or receivers
    <type>:
      version: 1.0.0
      description: <what it does, and its parameters>
      template: |
        <Collector configuration>
```

Pass blueprint files to NKTP as values files. How you do that depends on how you
deploy NKTP:

- [Argo CD](#argo-cd)
- [The `helm` command](#the-helm-command)
- [Flux](#flux)

### Argo CD

1. **Recommended: list your blueprint directories.** In your Argo CD
   Application, list NKTP's chart and the Git repository that holds your
   blueprints under `spec.sources`. Then add a pattern for each blueprint
   directory to the chart source's `helm.valueFiles`. You can list individual
   files and URLs there too:

   ```yaml
   spec:
     sources:
       - repoURL: https://github.com/dsx-ai-factory/nvke-telemetry-pipeline.git
         targetRevision: nktp/v0.2.0
         path: nktp
         helm:
           valueFiles:
             - $team/blueprints/*/*.yaml
             - $team/extra/team_backend.yaml
             - https://raw.githubusercontent.com/example/blueprints/v2.0.0/exporters/otlp_backend.yaml
             - $team/values.yaml
       - repoURL: https://github.com/example/team-config.git
         targetRevision: v2.0.0
         ref: team
   ```

   - `$team/blueprints/*/*.yaml` is a pattern that matches every blueprint file
     in the `blueprints` directory of your repository, the second source. Keep
     one blueprint per file, so adding a blueprint means adding a file. Argo CD
     reads the repository with the repository credentials configured in Argo CD,
     so it can be private. In that source:
     - `ref` is a short name you choose. `$team` refers to the root of the
       repository whose `ref` is `team`.
     - `targetRevision` is the tag, branch, or commit to read the files from. A
       tag or a commit keeps the files the same until you change this value. A
       branch, such as `main`, picks up every new commit.
     - Leave out `path`, or Argo CD also tries to deploy the repository's files.
   - `$team/extra/team_backend.yaml` is a single file in the same repository.
   - The URL is a single file that someone publishes. It must be publicly
     readable. Use a URL that includes a tag or commit, such as `v2.0.0` here,
     so the file changes only when you change the URL.
   - `$team/values.yaml` is your main values file.

   List your main values file last, as in the example, or keep your main values
   in `valuesObject`, which Argo CD applies after `valueFiles`. When two files
   define the same blueprint, the later one wins, so your main values can
   override any blueprint.
2. **If you can't change `valueFiles`**, paste the blueprints into your main
   values file, all under one `blueprints` key. The downside to this approach is
   that you maintain your own copy of each blueprint. To get a newer version of
   a published blueprint, you have to copy it again by hand, and your values
   file grows with every blueprint you add.

### The `helm` command

1. **Recommended: list the files.** Pass each blueprint file with `--values`.
   A file can be a local path or a URL that someone publishes. A URL must be
   publicly readable. Use a URL that includes a tag or commit, such as `v2.0.0`
   below, so the file changes only when you change the URL. Run this from the
   repository root after the steps in [Getting started](../getting-started.md),
   and edit the paths first:

   ```bash
   helm upgrade --install nktp ./nktp --namespace nktp \
     --values my-blueprints/team_backend.yaml \
     --values https://raw.githubusercontent.com/example/blueprints/v2.0.0/exporters/otlp_backend.yaml \
     --values my-values.yaml
   ```

   Pass your main values file last, as in the example. When two files define
   the same blueprint, the later one wins, so your main values file can
   override any blueprint.
2. **If you can't change the command**, paste the blueprints into your main
   values file, all under one `blueprints` key. The downside to this approach is
   that you maintain your own copy of each blueprint. To get a newer version of
   a published blueprint, you have to copy it again by hand, and your values
   file grows with every blueprint you add.

### Flux

1. **Recommended: list the files** under the NKTP HelmRelease's `valuesFrom`.
   Flux reads values only from ConfigMaps and Secrets, so first store each
   blueprint file in one, for example with Kustomize's `configMapGenerator`.
   Then name the ConfigMap and the key that holds each file:

   ```yaml
   spec:
     valuesFrom:
       - kind: ConfigMap
         name: blueprints
         valuesKey: team_backend.yaml
   ```

   Keep your main values in `values`, which Flux applies after `valuesFrom`, or
   list them last in `valuesFrom`. When two files define the same blueprint,
   the later one wins, so your main values can override any blueprint.
2. **If you can't change `valuesFrom`**, paste the blueprints into your main
   values, all under one `blueprints` key. The downside to this approach is that
   you maintain your own copy of each blueprint. To get a newer version of a
   published blueprint, you have to copy it again by hand, and your values grow
   with every blueprint you add.

### Where definitions apply

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

Each blueprint referenced in a pipeline becomes native components named after
it. These names also appear in the Collector's own metrics and logs:

```text
connectors:  forward/exp/otlp_backend
exporters:   otlp/(exp/otlp_backend)
pipelines:   logs/(exp/otlp_backend)
```
