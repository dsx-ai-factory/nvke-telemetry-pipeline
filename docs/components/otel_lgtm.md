# otel-lgtm

[grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) is an all-in-one observability backend (Grafana, Loki, Tempo, Prometheus) with OTLP ingest.
NKTP deploys it by default so that telemetry is viewable immediately after install.
Prometheus is available on port 9090 of the `lgtm` Service, for example
`http://lgtm.nktp.svc:9090` when installed in namespace `nktp`. Grafana is served on port 3000 of the `lgtm` service, with anonymous admin access (no login).

> [!WARNING]
> This container is intended for kicking the tires only. It will not support
> substantial scale and MUST be disabled for production use:
>
> ```yaml
> otel-lgtm:
>   enabled: false
> ```

## Dashboards

The default **NKTP Health** dashboard shows workload readiness, pipeline faults,
queues and telemetry flow. It includes component detail, diagnostic logs,
internal traces and links to the component dashboards. Workload readiness uses
kube-state-metrics through the metrics gateway. Collector and Target Allocator
self metrics use watchdog and do not require the metrics gateway. Target
Allocator counts stay separate for each replica. Disabling LGTM removes its dashboards. Target Allocator metrics
continue to flow to the backend configured in watchdog, which must support delta metrics.

Export and queue admission delays use internal spans through Tempo's TraceQL
metrics support. Container resource tables show requests and limits for up to
three containers per component. The dashboard also shows allocator allocation
duration and node-exporter diagnostics.

Each Collector also has its own dashboard with the same layout: its pipeline as
totals per signal, rows specific to it, its queues and resources, and its
self-telemetry sidecar. Panels that show replicas show the 20 largest. A
**Collectors** dropdown switches between them. The large-target dashboard
includes the kube-state-metrics scrape panels: samples, sample-count growth,
duration, watch errors and shard coverage.

See the [dashboards README](../../nktp/dashboards/README.md) for every
dashboard and how to import them into your own Grafana.

## Configuration

Configure the bundled backend under `otel-lgtm`:

| Value | Default | Purpose |
| --- | --- | --- |
| `resources` | `{}` | Container resource requests and limits. |
| `nodeSelector` | `{}` | Pod node selection. |
| `tolerations` | `[]` | Pod taint tolerations. |
| `extraVolumes` | `[]` | Kubernetes volumes appended to the built-in volumes. |
| `extraVolumeMounts` | `[]` | Container mounts appended to the built-in mounts. |

Additional container environment variables can be supplied through
`otel-lgtm.env`. To expose Loki and the backend Collector's process logs in
the `lgtm` container logs for troubleshooting:

```yaml
otel-lgtm:
  env:
    - name: ENABLE_LOGS_LOKI
      value: "true"
    - name: ENABLE_LOGS_OTELCOL
      value: "true"
```

NKTP disables Grafana's bundled Cloud Monitoring data source plugin (`stackdriver`) through
`GF_PLUGINS_DISABLE_PLUGINS`. To disable more plugins, set the same variable in `otel-lgtm.env` to a
comma-separated list, with `value` or `valueFrom`. NKTP renders a single entry at that position, with
`stackdriver` first and your list after it. JSON list syntax is not supported for this variable. NKTP does not
provision a Cloud Monitoring data source.

For custom backend configuration, create a ConfigMap in the release namespace
and mount its files at the paths used by the image. For example:

```yaml
otel-lgtm:
  extraVolumes:
    - name: backend-config
      configMap:
        name: my-lgtm-config
  extraVolumeMounts:
    - name: backend-config
      mountPath: /otel-lgtm/loki-config.yaml
      subPath: loki-config.yaml
      readOnly: true
```

NKTP uses the image's OTLP-only Tempo configuration by default. The chart
rejects `TEMPO_EXTRA_ARGS`, `BASH_ENV`, and `PATH` in `otel-lgtm.env`, whether
provided with `value` or `valueFrom`. It also rejects relative or
non-normalized mounts and mounts over `/`, `/otel-lgtm`, `tempo-config.yaml`,
the Tempo binary directory, and top-level `.sh` files under `/otel-lgtm`. Other
backend configuration mounts remain supported. These are best-effort
restrictions on chart inputs; they do not isolate Tempo from arbitrary
operator-supplied configurations, images, or executable content.

Use unique volume names and mount paths so the built-in dashboard and storage
mounts remain intact. Restart LGTM after changing a ConfigMap mounted with
`subPath`; those changes do not automatically roll the Deployment or update the
mounted files. Storage remains temporary. The scale profiles use these settings
to configure bundled LGTM and retain its dashboards without replacement
Service or Deployment definitions.
