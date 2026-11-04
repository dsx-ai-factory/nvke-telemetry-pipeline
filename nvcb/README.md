# NVCB (NVIDIA Collector Blueprints)

## Bring your own blueprints with Helm

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

Currently, NVCB is a Helm-only implementation. To deploy with Helm,
pass blueprint files to the chart as values files.
How you do that depends on how you deploy Helm charts:

- [Argo CD](#argo-cd)
- [The `helm` command](#the-helm-command)
- [Flux](#flux)

### Argo CD

1. **Recommended: list your blueprint directories.** In your Argo CD
   Application, add a pattern for each directory to the chart source's
   `helm.valueFiles`. You can list individual files, URLs, and directories in
   other Git repositories there too:

   ```yaml
   spec:
     sources:
       - repoURL: https://github.com/example/my-chart.git
         targetRevision: v1.4.0
         path: charts/my-chart
         helm:
           valueFiles:
             - blueprints/*/*.yaml
             - extra/team_backend.yaml
             - https://raw.githubusercontent.com/example/blueprints/v2.0.0/exporters/otlp_backend.yaml
             - $team/blueprints/*/*.yaml
             - values.yaml
       - repoURL: https://github.com/example/team-blueprints.git
         targetRevision: v2.0.0
         ref: team
   ```

   - `blueprints/*/*.yaml` is a pattern that matches every blueprint file in the
     `blueprints` directory. Keep one blueprint per file, so adding a blueprint
     means adding a file.
   - `extra/team_backend.yaml` is a single file. Patterns and file paths are
     relative to the chart's `path` in its repository.
   - The URL is a single file that someone publishes. It must be publicly
     readable. Use a URL that includes a tag or commit, such as `v2.0.0` here,
     so the file changes only when you change the URL.
   - `$team/blueprints/*/*.yaml` is a pattern in another Git repository, the
     second source. Use this for a whole directory or a private repository.
     Argo CD reads it with the repository credentials configured in Argo CD.
     In that source:
     - `ref` is a short name you choose. `$team` refers to the root of the
       repository whose `ref` is `team`.
     - `targetRevision` is the tag, branch, or commit to read the files from. A
       tag or a commit keeps the files the same until you change this value. A
       branch, such as `main`, picks up every new commit.
     - Leave out `path`, or Argo CD also tries to deploy the repository's files.

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
   below, so the file changes only when you change the URL. Edit the release
   name, chart, and paths before running:

   ```bash
   RELEASE=my-release
   CHART=./my-chart
   helm upgrade --install "$RELEASE" "$CHART" \
     --values blueprints/exporters/team_backend.yaml \
     --values https://raw.githubusercontent.com/example/blueprints/v2.0.0/exporters/otlp_backend.yaml \
     --values values.yaml
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

1. **Recommended: list the files** under your HelmRelease's `valuesFrom`. Flux
   reads values only from ConfigMaps and Secrets, so first store each blueprint
   file in one, for example with Kustomize's `configMapGenerator`. Then name the
   ConfigMap and the key that holds each file:

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

[NVCB design principles](design_principles.md) explains why NVCB works this way.
