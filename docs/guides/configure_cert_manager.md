# Configure cert-manager

With the default NKTP values, **install cert-manager before NKTP; no special
cert-manager configuration is needed.** NKTP and its Operator create the required
namespace-scoped Issuers and Certificates automatically. You do not need to create
an Issuer or ClusterIssuer yourself.

The rest of this guide is optional and covers the defaults and alternatives:

- **Use cert-manager (default and recommended).** cert-manager issues and renews
  certificates for internal Collector mTLS, the Operator's admission webhook,
  and Collector-to-Target-Allocator mTLS. These features are enabled by default
  and follow best practices for deploying NKTP.
- **Run without cert-manager.** Provide and renew your own certificates for
  internal Collector and Target Allocator mTLS. For the admission webhook, use
  your own certificates or let Helm generate them.

## TLS connections

The installation sections below configure certificates for three separate uses:

- **Internal mTLS:** protects telemetry sent from Collectors to gateways and
  watchdog. The cluster event reader, DaemonSet, and large-target metrics
  Collectors use client certificates; gateways use server certificates for
  incoming telemetry and client certificates for sidecar-to-watchdog traffic.
  Watchdog uses a server certificate.
- **Target Allocators:** mTLS protects each Collector's requests to its allocator,
  including credentials returned for authenticated ServiceMonitor/PodMonitor
  scrapes. Plaintext is possible, but credentials are withheld by default. See the
  Operator's [endpoint credential documentation](https://github.com/open-telemetry/opentelemetry-operator/blob/v0.161.0/docs/target-allocator/README.md#service--pod-monitor-endpoint-credentials).
- **Admission webhook:** the Kubernetes API server calls the Operator's webhook
  over HTTPS, which requires a trusted server certificate. See Kubernetes'
  [webhook connection requirements](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#contacting-the-webhook)
  and the Operator chart's [certificate options](https://github.com/open-telemetry/opentelemetry-helm-charts/blob/opentelemetry-operator-0.126.0/charts/opentelemetry-operator/README.md#tls-certificate-requirement).

Collector sidecars and the DaemonSet's Target Allocator and Operator log exporter
send to watchdog over OTLP/gRPC with mTLS on port 4317. Target Allocator exporters
send their own metrics over plaintext OTLP/gRPC on port 4320.

**NOTE:** Gateway-to-backend TLS is outside this guide's scope and must be
configured separately.

## Installing NKTP with cert-manager

Install cert-manager before installing NKTP with default values, following the
[cert-manager installation instructions](https://cert-manager.io/docs/installation/).
NKTP does not bundle or install cert-manager.

### Internal mTLS

cert-manager creates a self-signed CA and issuer, and issues and renews
certificates for internal mTLS.

To use an existing issuer, set `certManager.internalTLS.issuerRef` to a CA `Issuer`
or `ClusterIssuer` that populates `ca.crt` in the generated TLS Secrets, alongside
`tls.crt` and `tls.key`. NKTP mounts `ca.crt` to verify server and client certificates.
ACME issuers are unsupported for internal mTLS: they do not populate `ca.crt`,
so the required Secret volume mounts fail. See cert-manager's
[certificate Secret documentation](https://cert-manager.io/docs/usage/certificate/#target-secret).
Leave `issuerRef: {}` to use NKTP's generated internal CA and issuer.

Default values:

```yaml
certManager:
  internalTLS:
    enabled: true
    issuerRef: {}
```

### Target Allocators

cert-manager creates a separate CA for each Collector–Target Allocator pair and
issues and renews its server and client certificates, as described in
[With cert-manager (default)](https://github.com/open-telemetry/opentelemetry-operator/blob/v0.161.0/docs/target-allocator/README.md#with-cert-manager-default).

`certManager.targetAllocators.enabled` sets `mtls.useCertManager` for the
`watchdog`, `daemonset`, and `large_target_metrics` Target Allocators. Its default
of `true` makes `mtls.useCertManager` default to `true` for all three; each
allocator can override it. Separately, `mtls.enabled` defaults to `true` per
allocator and is unaffected by this setting.

Default values:

```yaml
certManager:
  targetAllocators:
    enabled: true
```

### Admission webhook

cert-manager issues and renews the Operator's self-signed admission webhook
certificate, independently of the internal and Target Allocator CAs.

Default values:

```yaml
opentelemetry-kube-stack:
  opentelemetry-operator:
    admissionWebhooks:
      certManager:
        enabled: true
```

### Expected outcome

After installing NKTP with the default cert-manager settings, expect all Issuers
and Certificates to be Ready. For a release in the `nktp` namespace:

```bash
kubectl get issuers,certs -n nktp
```

Example output with cert-manager for all components:

```text
NAME                                                                  READY   AGE
issuer.cert-manager.io/nktp-daemonset-ca-issuer                       True    3m10s
issuer.cert-manager.io/nktp-daemonset-self-signed-issuer              True    3m10s
issuer.cert-manager.io/nktp-internal-ca-issuer                        True    3m43s
issuer.cert-manager.io/nktp-internal-selfsigned-issuer                True    3m43s
issuer.cert-manager.io/nktp-large-target-metrics-ca-issuer            True    3m10s
issuer.cert-manager.io/nktp-large-target-metrics-self-signed-issuer   True    3m10s
issuer.cert-manager.io/nktp-operator-selfsigned-issuer                True    3m43s
issuer.cert-manager.io/nktp-watchdog-ca-issuer                        True    3m10s
issuer.cert-manager.io/nktp-watchdog-self-signed-issuer               True    3m10s

NAME                                                                   READY   SECRET                                          AGE
certificate.cert-manager.io/nktp-cluster-events-client                 True    nktp-cluster-events-client-tls                  3m43s
certificate.cert-manager.io/nktp-daemonset-ca-cert                     True    nktp-daemonset-ca-cert                          3m10s
certificate.cert-manager.io/nktp-daemonset-client                      True    nktp-daemonset-client-tls                       3m43s
certificate.cert-manager.io/nktp-daemonset-ta-client-cert              True    nktp-daemonset-ta-client-cert                   3m10s
certificate.cert-manager.io/nktp-daemonset-ta-server-cert              True    nktp-daemonset-ta-server-cert                   3m10s
certificate.cert-manager.io/nktp-gateway-logs-client                   True    nktp-gateway-logs-client-tls                    3m43s
certificate.cert-manager.io/nktp-gateway-logs-server                   True    nktp-gateway-logs-server-tls                    3m43s
certificate.cert-manager.io/nktp-gateway-metrics-client                True    nktp-gateway-metrics-client-tls                 3m43s
certificate.cert-manager.io/nktp-gateway-metrics-server                True    nktp-gateway-metrics-server-tls                 3m43s
certificate.cert-manager.io/nktp-gateway-traces-client                 True    nktp-gateway-traces-client-tls                  3m43s
certificate.cert-manager.io/nktp-gateway-traces-server                 True    nktp-gateway-traces-server-tls                  3m43s
certificate.cert-manager.io/nktp-internal-ca                           True    nktp-internal-ca                                3m43s
certificate.cert-manager.io/nktp-large-target-metrics-ca-cert          True    nktp-large-target-metrics-ca-cert               3m10s
certificate.cert-manager.io/nktp-large-target-metrics-client           True    nktp-large-target-metrics-client-tls            3m43s
certificate.cert-manager.io/nktp-large-target-metrics-ta-client-cert   True    nktp-large-target-metrics-ta-client-cert        3m10s
certificate.cert-manager.io/nktp-large-target-metrics-ta-server-cert   True    nktp-large-target-metrics-ta-server-cert        3m10s
certificate.cert-manager.io/nktp-operator-serving-cert                 True    nktp-operator-controller-manager-service-cert   3m43s
certificate.cert-manager.io/nktp-watchdog-ca-cert                      True    nktp-watchdog-ca-cert                           3m10s
certificate.cert-manager.io/nktp-watchdog-server                       True    nktp-watchdog-server-tls                        3m43s
certificate.cert-manager.io/nktp-watchdog-ta-client-cert               True    nktp-watchdog-ta-client-cert                    3m10s
certificate.cert-manager.io/nktp-watchdog-ta-server-cert               True    nktp-watchdog-ta-server-cert                    3m10s
```

## Installing NKTP without cert-manager

This guide assumes the release namespace already exists and is configured for
NKTP, as described in [Getting started](../getting-started.md).

For a new installation, follow the steps below to provide certificates for all
three TLS connections **before running `helm install`**. This avoids Pods waiting
for missing Secrets. Sections labeled **Alternative** are optional changes to
this setup; otherwise, continue to the next section.

The certificate commands are based on
[OpenTelemetry's certificate-generation example](https://opentelemetry.io/docs/collector/configuration/#configuring-certificates).

> **WARNING: The certificates are for illustration only and expire after seven days.**
> For production, use your organization's PKI or a managed private CA for issuance
> and renewal, with automated Secret delivery such as
> [External Secrets Operator](https://external-secrets.io/latest/). These commands
> do not provide automatic renewal or certificate reloads.

Never commit generated keys, Secrets, or rendered manifests.

Replace `<namespace>` with the release namespace and create a separate certificate
and Secret for each component or signal listed. The SANs match the chart's default
connection endpoints. The `nktp-local-` names distinguish user-provided resources
from cert-manager-managed resources. Internal Collector Secrets retain their
default names to match the existing mounts.

| TLS connection | CA identifier | Certificate | Component or signal | Server DNS SAN | Extended key usage | Secret name |
| --- | --- | --- | --- | --- | --- | --- |
| Internal mTLS | `internal` | Watchdog server | `watchdog` | `nktp-watchdog-collector` | `serverAuth` | `nktp-watchdog-server-tls` |
| Internal mTLS | `internal` | Gateway server | `<signal>`: `logs`, `metrics`, `traces` | `nktp-gateway-<signal>-collector` | `serverAuth` | `nktp-gateway-<signal>-server-tls` |
| Internal mTLS | `internal` | Collector client | `<component>`: `daemonset`, `large-target-metrics`, `cluster-events` | — | `clientAuth` | `nktp-<component>-client-tls` |
| Internal mTLS | `internal` | Gateway sidecar client | `<signal>`: `logs`, `metrics`, `traces` | — | `clientAuth` | `nktp-gateway-<signal>-client-tls` |
| Target Allocators | `ta-<component>` | Target Allocator server | `<component>`: `watchdog`, `daemonset`, `large-target-metrics` | `nktp-<component>-targetallocator.<namespace>.svc` | `serverAuth` | `nktp-local-<component>-targetallocator-server` |
| Target Allocators | `ta-<component>` | Target Allocator client | `<component>`: `watchdog`, `daemonset`, `large-target-metrics` | — | `clientAuth` | `nktp-local-<component>-targetallocator-client` |
| Admission webhook | `webhook` | Admission webhook server | Operator | `nktp-operator-webhook.<namespace>.svc` | `serverAuth` | `nktp-local-webhook-tls` |

### Prepare certificates

Run the commands in the same shell. You need `cfssl`, `cfssljson`, and kubectl.
Edit the assignments for your cluster and add the YAML settings below to your
NKTP values file.

```bash
set -euo pipefail
KUBECONFIG_FILE=/path/to/kubeconfig
CONTEXT=kind-my-cluster
NAMESPACE=nktp
RELEASE=nktp
umask 077
CERT_DIR="$(mktemp -d /tmp/nktp-tls.XXXXXX)"

k() {
  kubectl --kubeconfig "$KUBECONFIG_FILE" --context "$CONTEXT" \
    --namespace "$NAMESPACE" "$@"
}
```

Include these settings in your values file:

```yaml
certManager:
  internalTLS:
    enabled: false
  targetAllocators:
    enabled: false
opentelemetry-kube-stack:
  opentelemetry-operator:
    admissionWebhooks:
      certManager:
        enabled: false
      autoGenerateCert:
        enabled: true
        recreate: false
      secretName: nktp-local-webhook-tls
      secretLabels:
        nktp.nvidia.io/local-certificates: "true"
```

Create separate CAs for internal mTLS, each Target Allocator pair, and the webhook,
matching the trust separation of the cert-manager setup:

> **WARNING: The certificates are for illustration only and expire after seven days.**
> Use managed issuance, renewal, and Secret delivery for production.

```bash
for ca in internal ta-watchdog ta-daemonset ta-large-target-metrics webhook; do
  cfssl genkey -initca - <<EOF | cfssljson -bare "$CERT_DIR/$ca-ca"
{
  "CN": "nktp-example-$ca-ca",
  "key": {"algo": "rsa", "size": 2048},
  "ca": {"expiry": "168h"}
}
EOF
done

cat > "$CERT_DIR/signing.json" <<'EOF'
{
  "signing": {
    "profiles": {
      "serverAuth": {
        "usages": ["signing", "key encipherment", "server auth"],
        "expiry": "168h"
      },
      "clientAuth": {
        "usages": ["signing", "key encipherment", "client auth"],
        "expiry": "168h"
      }
    }
  }
}
EOF
```

Define helpers that sign with the selected CA and include only that CA in the
Secret:

```bash
issue_certificate() {
  local ca="$1" name="$2" usage="$3" san="${4:-}"
  cfssl gencert -ca "$CERT_DIR/$ca-ca.pem" -ca-key "$CERT_DIR/$ca-ca-key.pem" \
    -config "$CERT_DIR/signing.json" -profile "$usage" \
    -hostname "$san" - <<EOF | cfssljson -bare "$CERT_DIR/$name"
{
  "CN": "$name",
  "hosts": [],
  "key": {"algo": "rsa", "size": 2048}
}
EOF
}

create_tls_secret() {
  local ca="$1" name="$2"
  k create secret generic "$name" --type=kubernetes.io/tls \
    --from-file="ca.crt=$CERT_DIR/$ca-ca.pem" \
    --from-file="tls.crt=$CERT_DIR/$name.pem" \
    --from-file="tls.key=$CERT_DIR/$name-key.pem"
  k label secret "$name" nktp.nvidia.io/local-certificates=true
}
```

For the watchdog server row, generate its certificate and create its Secret:

> **WARNING: The certificates are for illustration only and expire after seven days.**

```bash
CA_NAME=internal
SECRET_NAME=nktp-watchdog-server-tls
issue_certificate "$CA_NAME" "$SECRET_NAME" serverAuth nktp-watchdog-collector
create_tls_secret "$CA_NAME" "$SECRET_NAME"
```

Repeat for all remaining table entries using their CA identifier, Secret name,
usage, and SAN. For client certificates, omit the fourth argument to
`issue_certificate`.

### Internal mTLS

The internal Collector Secret names match the chart's default mounts and preserve
internal mTLS.

#### Alternative: Switching internal traffic to plaintext

Internal mTLS is recommended to protect telemetry, but Collectors can use
plaintext. To disable internal mTLS, use the
[plaintext example](../examples/disable_internal_mtls.yaml) alongside the Target
Allocator and webhook settings below. The Secrets in the four table rows labeled
**Internal mTLS** (Watchdog server, Gateway server, Collector client, and Gateway
sidecar client) become unused and may be deleted.

### Target Allocators

Create a CA ConfigMap for each Collector–Target Allocator pair:

```bash
for component in watchdog daemonset large-target-metrics; do
  k create configmap "nktp-local-$component-ta-ca" \
    --from-file="ca.crt=$CERT_DIR/ta-$component-ca.pem"
  k label configmap "nktp-local-$component-ta-ca" nktp.nvidia.io/local-certificates=true
done
```

Configure `mtls` for each workload at `watchdog.kubernetes.targetAllocator`,
`daemonset.kubernetes.targetAllocator`, and
`large_target_metrics.kubernetes.targetAllocator`, using the corresponding Secret
names from the table and the matching CA ConfigMap. For example, include this
DaemonSet configuration in your values file:

```yaml
daemonset:
  kubernetes:
    targetAllocator:
      mtls:
        enabled: true
        tls:
          certificateAuthorityCertificate:
            configMap:
              name: nktp-local-daemonset-ta-ca
              key: ca.crt
          serverCertificate:
            certificateSecret:
              name: nktp-local-daemonset-targetallocator-server
              key: tls.crt
            keySecret:
              name: nktp-local-daemonset-targetallocator-server
              key: tls.key
          clientCertificate:
            certificateSecret:
              name: nktp-local-daemonset-targetallocator-client
              key: tls.crt
            keySecret:
              name: nktp-local-daemonset-targetallocator-client
              key: tls.key
```

See the Operator's
[user-provided certificate instructions](https://github.com/open-telemetry/opentelemetry-operator/blob/v0.161.0/docs/target-allocator/README.md#without-cert-manager-user-provided-certificates)
for the reference format.

#### Alternative: Switching Target Allocators to plaintext

**Warning: authenticated scrapes that rely on credentials delivered by the Target
Allocator will fail.** With `allowInsecureAuthSecrets: false`, the allocator
withholds credential values on HTTP. The upstream
[`allowInsecureAuthSecrets` option](https://github.com/open-telemetry/opentelemetry-operator/blob/v0.161.0/docs/target-allocator/README.md#alternative-allow-insecure-auth-secrets)
is intended only for transport already secured by a service mesh or equivalent;
keep it disabled here.

For HTTP, replace the allocator settings above for each of `watchdog`, `daemonset`,
and `large_target_metrics` with this pattern (DaemonSet shown). The Secrets in the
two table rows labeled **Target Allocators** and the three allocator CA ConfigMaps
become unused and may be deleted.

```yaml
daemonset:
  kubernetes:
    targetAllocator:
      allowInsecureAuthSecrets: false
      mtls:
        enabled: false
```

### Admission webhook

After creating `nktp-local-webhook-tls` using the Secret example above, let Helm
adopt it:

```bash
k label secret nktp-local-webhook-tls app.kubernetes.io/managed-by=Helm
k annotate secret nktp-local-webhook-tls \
  "meta.helm.sh/release-name=$RELEASE" \
  "meta.helm.sh/release-namespace=$NAMESPACE"
```

With `autoGenerateCert.enabled: true` and `recreate: false`, Helm reuses this
Secret and configures the webhook's CA trust.

#### Alternative: Switching to Helm-managed certificates

To let Helm generate the webhook certificate instead, delete the Secret created
above **before installing NKTP**:

```bash
k delete secret nktp-local-webhook-tls
```

Keep `autoGenerateCert.enabled: true` and `recreate: false` in your values. Helm
creates and reuses a self-signed certificate that expires after 365 days by default
and is not renewed automatically.

### Expected outcome

After installing NKTP with user-provided certificates for all components, expect
the following certificate Secrets and CA ConfigMaps:

```bash
k get secrets -l nktp.nvidia.io/local-certificates=true
k get configmaps -l nktp.nvidia.io/local-certificates=true
```

Example output with user-provided certificates for all components:

```text
NAME                                                     TYPE                DATA   AGE
nktp-cluster-events-client-tls                           kubernetes.io/tls   3      14s
nktp-daemonset-client-tls                                kubernetes.io/tls   3      14s
nktp-gateway-logs-client-tls                             kubernetes.io/tls   3      14s
nktp-gateway-logs-server-tls                             kubernetes.io/tls   3      14s
nktp-gateway-metrics-client-tls                          kubernetes.io/tls   3      14s
nktp-gateway-metrics-server-tls                          kubernetes.io/tls   3      14s
nktp-gateway-traces-client-tls                           kubernetes.io/tls   3      14s
nktp-gateway-traces-server-tls                           kubernetes.io/tls   3      14s
nktp-large-target-metrics-client-tls                     kubernetes.io/tls   3      14s
nktp-local-daemonset-targetallocator-client              kubernetes.io/tls   3      14s
nktp-local-daemonset-targetallocator-server              kubernetes.io/tls   3      14s
nktp-local-large-target-metrics-targetallocator-client   kubernetes.io/tls   3      14s
nktp-local-large-target-metrics-targetallocator-server   kubernetes.io/tls   3      14s
nktp-local-watchdog-targetallocator-client               kubernetes.io/tls   3      14s
nktp-local-watchdog-targetallocator-server               kubernetes.io/tls   3      14s
nktp-local-webhook-tls                                   kubernetes.io/tls   3      14s
nktp-watchdog-server-tls                                 kubernetes.io/tls   3      14s

NAME                                     DATA   AGE
nktp-local-daemonset-ta-ca                 1      14s
nktp-local-large-target-metrics-ta-ca      1      14s
nktp-local-watchdog-ta-ca                  1      14s
```
