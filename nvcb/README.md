# NVCB (NVIDIA Collector Blueprints)

NVIDIA Collector Blueprints is a library chart that allows you to define OpenTelemetry collector config fragments ("blueprints") and reference them alongside normal OpenTelemetry receiver/exporter components. The library takes a config that includes blueprint definitions and expands it into a fully native OpenTelemetry config. The blueprints layer is entirely optional; native config without blueprints passes through unchanged.

## Prerequisites

If you are using a custom collector build, ensure that the `forwardconnector` component is added to your OCB config.

## Usage

Add the `nvcb` library chart as a dependency to your `Chart.yaml`:

```yaml
apiVersion: v2
name: my-chart
description: my chart
type: application
version: 0.0.0
dependencies:
  - name: nvcb
    version: "0.1.0"
    repository: "https://helm.ngc.nvidia.com/<org-or-org/team>"
```

Pass the Collector config map into `nvcb.run`, not the Helm root context (`.`). This is the map containing `receivers`, `processors`, `exporters`, `service`, and any other native Collector settings. If you use blueprints, their definitions must be under a `blueprints` key in that same map.

`nvcb.run` returns a native Collector config as a YAML string, not a Kubernetes resource. Embed that string in your resource using `nindent`, like this ConfigMap in `templates/collector-config.yaml`.

**Option 1: Blueprints defined within the Collector config**

Here, both the config and its `blueprints` key live under `.Values.myCollector.config`:

```yaml
{{- $config := .Values.myCollector.config -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-collector-config
data:
  collector.yaml: |
    {{- include "nvcb.run" $config | nindent 4 }}
```

**Option 2: Blueprints defined separately in values**

If the definitions live under `.Values.blueprints` instead, replace the first line above with:

```gotemplate
{{- $config := deepCopy .Values.myCollector.config -}}
{{- $_ := set $config "blueprints" .Values.blueprints -}}
```

This adds the definitions under `$config.blueprints`, keeping the rest of the config intact and leaving `.Values.myCollector.config` unchanged. The ConfigMap still passes `$config` into `nvcb.run`. Use this option when the config does not already contain its own blueprint definitions.

## Learn more
- See [Blueprints](./docs/blueprints-intro.md) to learn more about how blueprints work.
- See [Create Blueprints](./docs/create-a-blueprint.md) to create your own blueprint.
- See [Test Cases](https://github.com/dsx-ai-factory/nvke-telemetry-pipeline/tree/main/nvcb/tests) for blueprint success and failure scenarios and general limitations.
- See [NVCB design principles](design_principles.md) to better understand the interface NVCB implements and why.
