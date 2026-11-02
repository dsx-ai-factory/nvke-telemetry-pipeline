# Third-Party Notices

The packaged `nktp` Helm chart bundles the following unmodified upstream Helm
charts as dependencies (see `Chart.yaml` and `Chart.lock`). Each is licensed
under the Apache License, Version 2.0; a copy of that license is included in
this chart as `LICENSE`.

Container images deployed by these charts are pulled from their upstream
registries at install time and are not redistributed with this chart.

| Chart | Version | Copyright | License | Source |
|---|---|---|---|---|
| opentelemetry-kube-stack | 0.25.1 | Copyright The OpenTelemetry Authors | Apache-2.0 | https://github.com/open-telemetry/opentelemetry-helm-charts |
| opentelemetry-kube-stack / opentelemetry-operator | 0.126.0 | Copyright The OpenTelemetry Authors | Apache-2.0 | https://github.com/open-telemetry/opentelemetry-helm-charts |
| opentelemetry-kube-stack / otel-crds | bundled in opentelemetry-kube-stack 0.25.1 | Copyright The OpenTelemetry Authors | Apache-2.0 | https://github.com/open-telemetry/opentelemetry-helm-charts |
| opentelemetry-kube-stack / prometheus-crds | bundled in opentelemetry-kube-stack 0.25.1 | Copyright The OpenTelemetry Authors; CRDs Copyright The prometheus-operator Authors | Apache-2.0 | https://github.com/open-telemetry/opentelemetry-helm-charts, https://github.com/prometheus-operator/prometheus-operator |
| opentelemetry-kube-stack / kube-state-metrics | 6.3.0 | Copyright The Prometheus Community Authors | Apache-2.0 | https://github.com/prometheus-community/helm-charts |
| opentelemetry-kube-stack / prometheus-node-exporter | 4.48.0 | Copyright The Prometheus Community Authors | Apache-2.0 | https://github.com/prometheus-community/helm-charts |
| prometheus-node-exporter | 4.56.1 | Copyright The Prometheus Community Authors | Apache-2.0 | https://github.com/prometheus-community/helm-charts |
| metrics-server | 3.13.1 | Copyright The Kubernetes Authors | Apache-2.0 | https://github.com/kubernetes-sigs/metrics-server |
| prometheus-blackbox-exporter | 11.20.2 | Copyright The Prometheus Community Authors | Apache-2.0 | https://github.com/prometheus-community/helm-charts |

## Upstream NOTICE files

The `prometheus-crds` chart contains CustomResourceDefinitions from
prometheus-operator, which carries the following NOTICE:

```
CoreOS Project
Copyright 2015 CoreOS, Inc

This product includes software developed at CoreOS, Inc.
(http://www.coreos.com/).
```

Update this file when a dependency in `Chart.yaml` changes version.
