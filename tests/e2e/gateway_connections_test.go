// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/yaml"
)

// TestGatewayConnections checks that gRPC connection rotation lets existing
// Collectors reach new gateway replicas while all clients keep delivering.
// Isolated fixtures use the chart's connection settings with shorter intervals.
// A control without rotation must use only the original gateway for one minute.
// Cleanup removes the fixture namespace and its resources.
func TestGatewayConnections(t *testing.T) {
	collectors := map[string]*collectorSpec{}
	var k kubernetes.Interface
	f := features.New("Gateway connection rotation").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			var err error
			k, err = kubernetes.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating Kubernetes client")
			renderCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			args := []string{"template", testenv.ReleaseName, testenv.ChartPath, "--namespace", c.Namespace(), "--values", testenv.ValuesPath}
			for _, template := range []string{"gateway-logs", "gateway-metrics", "gateway-traces", "daemonset"} {
				args = append(args, "--show-only", "templates/"+template+".yaml")
			}
			rendered, err := exec.CommandContext(renderCtx, "helm", args...).Output()
			require.NoError(t, err, "rendering the chart's connection settings")
			decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
			for {
				var object struct {
					Metadata metav1.ObjectMeta `json:"metadata"`
					Spec     *collectorSpec    `json:"spec"`
				}
				err := decoder.Decode(&object)
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
				require.NotNil(t, object.Spec)
				collectors[object.Metadata.Name] = object.Spec
			}

			for _, name := range []string{"nktp-gateway-logs", "nktp-gateway-metrics", "nktp-gateway-traces"} {
				configured := collectors[name]
				require.NotNil(t, configured)
				require.NotNil(t, configured.Config.Receivers.OTLP)
				listener, found := configured.Config.Receivers.OTLP.Protocols["grpc"]
				require.True(t, found)
				require.NotNil(t, listener.Keepalive)
				require.NotNil(t, listener.Keepalive.ServerParameters)
				age, err := time.ParseDuration(listener.Keepalive.ServerParameters.MaxConnectionAge)
				require.NoError(t, err, "%s max_connection_age must be a duration", name)
				require.Positive(t, age, "%s must enable connection rotation", name)
			}
			return ctx
		})
	const recentWindow = 5 * time.Second

	for _, tc := range []struct {
		name     string
		rotate   bool
		protocol string
	}{
		{name: "grpc_with_max_connection_age", rotate: true, protocol: "grpc"},
		{name: "grpc_without_max_connection_age", protocol: "grpc"},
	} {
		f.Assess(tc.name, func(featureCtx context.Context, t *testing.T, c *envconf.Config) context.Context {
			ctx, cancel := context.WithTimeout(featureCtx, 15*time.Minute)
			defer cancel()
			clientCount := 1
			if tc.rotate {
				clientCount = 8
			}
			gatewayCount := 1
			expectedDeliveries := clientCount * len(connectionSignals)
			clientCollector := collectors["nktp-daemonset"]
			require.NotNil(t, clientCollector)
			fixtureGateway := collectors["nktp-gateway-logs"]
			protocol := tc.protocol
			listener, found := fixtureGateway.Config.Receivers.OTLP.Protocols[protocol]
			require.True(t, found, "logs gateway must accept %s", protocol)
			_, portText, err := net.SplitHostPort(listener.Endpoint)
			require.NoError(t, err, "parsing gateway listener %q", listener.Endpoint)
			portNumber, err := strconv.ParseUint(portText, 10, 16)
			require.NoError(t, err)
			require.Positive(t, portNumber)
			port := int32(portNumber)
			ns, err := k.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "nktp-e2e-connections-"},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			stage := "creating fixture"
			lastReport := time.Now()
			reportPods := func(pods []corev1.Pod) {
				if time.Since(lastReport) < 15*time.Second {
					return
				}
				ready := map[string]int{}
				var waiting []string
				for _, pod := range pods {
					isReady := len(pod.Status.ContainerStatuses) == len(pod.Spec.Containers)
					for _, status := range pod.Status.ContainerStatuses {
						isReady = isReady && status.Ready
					}
					if isReady {
						ready[pod.Labels["role"]]++
					} else {
						waiting = append(waiting, pod.Name)
					}
				}
				t.Logf("%s: clients ready=%d/%d; gateways ready=%d/%d; waiting=%v",
					stage, ready["client"], clientCount, ready["gateway"], gatewayCount, waiting)
				lastReport = time.Now()
			}
			t.Logf("%s: namespace=%s; expected client pods=%d", tc.name, ns.Name, clientCount)
			t.Cleanup(func() {
				if t.Failed() {
					diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
					logConnectionNodeCapacity(diagnosticCtx, t, k)
					logConnectionFailure(diagnosticCtx, t, k, ns.Name, stage)
					cancel()
				}
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(featureCtx), 15*time.Minute)
				defer cancel()
				err := k.CoreV1().Namespaces().Delete(cleanupCtx, ns.Name, metav1.DeleteOptions{
					Preconditions: &metav1.Preconditions{UID: &ns.UID},
				})
				if !apierrors.IsNotFound(err) {
					assert.NoError(t, err, "deleting fixture namespace %s", ns.Name)
				}
				assert.NoError(t, wait.PollUntilContextCancel(cleanupCtx, time.Second, true, func(ctx context.Context) (bool, error) {
					pods, err := k.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{})
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return err == nil && len(pods.Items) == 0, err
				}), "waiting for fixture pods to be removed")
			})

			require.NotNil(t, listener.TLS, "gateway connections must retain mTLS")
			// Substitute fixture credentials while preserving TLS policy settings.
			serverTLS := *listener.TLS
			serverTLS.CertFile = "/etc/nktp/internal-tls/server/tls.crt"
			serverTLS.KeyFile = "/etc/nktp/internal-tls/server/tls.key"
			serverTLS.ClientCAFile = "/etc/nktp/internal-tls/server/ca.crt"
			serverTLS.CAPEM, serverTLS.CertPEM, serverTLS.KeyPEM = "", "", ""
			if serverTLS.CAFile != "" {
				serverTLS.CAFile = "/etc/nktp/internal-tls/server/ca.crt"
			}
			listener.TLS = &serverTLS
			if protocol == "grpc" {
				// Shorten rotation without mutating the defaults used by other cases.
				require.NotNil(t, listener.Keepalive)
				keepalive := *listener.Keepalive
				parameters := *keepalive.ServerParameters
				keepalive.ServerParameters = &parameters
				listener.Keepalive = &keepalive
				listener.Keepalive.ServerParameters.MaxConnectionAge = "5s"
				listener.Keepalive.ServerParameters.MaxConnectionAgeGrace = "2s"
				if !tc.rotate {
					listener.Keepalive.ServerParameters.MaxConnectionAge = ""
				}
			}
			gatewayConfig := collectorConfig{
				Receivers: collectorReceivers{OTLP: &otlpReceiver{
					Protocols: map[string]otlpListener{protocol: listener},
				}},
				Exporters: map[string]collectorExporter{
					"debug": {Verbosity: "detailed", SamplingInitial: 10, SamplingThereafter: 1},
				},
				Service: collectorService{Pipelines: map[string]collectorPipeline{
					"logs":    {Receivers: []string{"otlp"}, Exporters: []string{"debug"}},
					"metrics": {Receivers: []string{"otlp"}, Exporters: []string{"debug"}},
					"traces":  {Receivers: []string{"otlp"}, Exporters: []string{"debug"}},
				}},
			}
			var clientConfig collectorConfig
			// Fixed fixture components stay in native Collector YAML.
			require.NoError(t, yaml.Unmarshal([]byte(`
receivers:
  file_log:
    include: [/logs/input.log]
    start_at: beginning
    retry_on_failure: {enabled: true, max_elapsed_time: 0s}
  prometheus:
    config:
      scrape_configs:
        - job_name: connection-test
          scrape_interval: 1s
          scrape_timeout: 1s
          static_configs:
            - targets: [127.0.0.1:8888]
              labels: {client_id: 'rotation-metrics-${env:CLIENT_ID}'}
  # The generator posts locally; only the Collector exports to the Service.
  otlp:
    protocols:
      http: {endpoint: '127.0.0.1:4319'}
exporters: {}
service:
  pipelines: {}
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus: {host: '127.0.0.1', port: 8888}
`), &clientConfig))
			for signal, receiver := range map[string]string{"logs": "file_log", "metrics": "prometheus", "traces": "otlp"} {
				exporterName := "otlp_grpc/" + signal
				exporter, found := clientCollector.Config.Exporters[exporterName]
				require.True(t, found, "the chart must configure %s", exporterName)
				require.NotEmpty(t, exporter.Endpoint, "%s must have a destination", exporterName)
				require.NotNil(t, exporter.TLS, "%s must retain mTLS", exporterName)
				clientTLS := *exporter.TLS
				clientTLS.CAFile = "/etc/nktp/internal-tls/client/ca.crt"
				clientTLS.CertFile = "/etc/nktp/internal-tls/client/tls.crt"
				clientTLS.KeyFile = "/etc/nktp/internal-tls/client/tls.key"
				clientTLS.CAPEM, clientTLS.CertPEM, clientTLS.KeyPEM = "", "", ""
				if clientTLS.ServerNameOverride != "" {
					clientTLS.ServerNameOverride = "gateway." + ns.Name + ".svc"
				}
				exporter.TLS = &clientTLS
				exporter.Endpoint = net.JoinHostPort("gateway."+ns.Name+".svc", portText)
				if exporter.Authority != "" {
					exporter.Authority = exporter.Endpoint
				}
				clientConfig.Exporters[exporterName] = exporter
				clientConfig.Service.Pipelines[signal] = collectorPipeline{
					Receivers: []string{receiver}, Exporters: []string{exporterName},
				}
			}
			for name, config := range map[string]collectorConfig{"gateway": gatewayConfig, "clients": clientConfig} {
				data, err := yaml.Marshal(config)
				require.NoError(t, err)
				_, err = k.CoreV1().ConfigMaps(ns.Name).Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string]string{"config.yaml": string(data)},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			}
			_, err = k.CoreV1().Services(ns.Name).Create(ctx, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "gateway"},
				Spec: corev1.ServiceSpec{
					Selector: map[string]string{"role": "gateway"}, SessionAffinity: corev1.ServiceAffinityNone,
					Ports: []corev1.ServicePort{{Name: protocol, Port: port, TargetPort: intstr.FromInt32(port)}},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			createConnectionTLS(ctx, t, k, ns.Name)
			createConnectionGateway(ctx, t, k, ns.Name, "gateway-0", fixtureGateway.Image, port)
			createConnectionClients(ctx, t, k, ns.Name, clientCollector.Image, clientCount)
			stage = "waiting for initial gateway and client pods"
			require.NoError(t, waitConnectionPods(ctx, k, ns.Name, clientCount+gatewayCount, nil, reportPods), stage)
			pods, err := k.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			// A replacement Pod opens new connections and would invalidate the result.
			identities := map[string]types.UID{}
			for _, pod := range pods.Items {
				identities[pod.Name] = pod.UID
			}
			stage = "waiting for the original gateway Service endpoint"
			require.NoError(t, waitGatewayEndpoints(ctx, k, ns.Name, gatewayCount), stage)
			// Establish every signal's connection before adding another destination.
			stage = "waiting for every client to deliver every signal to gateway-0"
			var baseline map[string]time.Time
			err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
				var err error
				baseline, err = gatewayClients(ctx, k, ns.Name, "gateway-0", time.Now().Add(-recentWindow))
				return len(baseline) == expectedDeliveries, err
			})
			if err != nil {
				t.Logf("initial delivery: %s", connectionDeliverySummary(baseline, nil, clientCount))
			}
			require.NoError(t, err, stage)
			stage = "adding gateway-1 and waiting for its pod"
			createConnectionGateway(ctx, t, k, ns.Name, "gateway-1", fixtureGateway.Image, port)
			gatewayCount++
			require.NoError(t, waitConnectionPods(ctx, k, ns.Name, clientCount+gatewayCount, identities, reportPods), stage)
			stage = "waiting for both gateway Service endpoints"
			require.NoError(t, waitGatewayEndpoints(ctx, k, ns.Name, gatewayCount), stage)

			// Three short rotation periods leave multiple opportunities for
			// random Service selection to reach the new replica.
			observationPeriod := 20 * time.Second
			if !tc.rotate {
				observationPeriod = time.Minute
			}
			observeCtx, stop := context.WithTimeout(ctx, observationPeriod)
			defer stop()
			started := time.Now()
			stage = "observing delivery after gateway scale-out"
			var first, second map[string]time.Time
			err = wait.PollUntilContextCancel(observeCtx, time.Second, true, func(pollCtx context.Context) (bool, error) {
				probeParent := pollCtx
				if !tc.rotate {
					probeParent = ctx
				}
				probeCtx, cancel := context.WithTimeout(probeParent, 15*time.Second)
				defer cancel()
				if err := waitConnectionPods(probeCtx, k, ns.Name, clientCount+gatewayCount, identities, reportPods); err != nil {
					return false, err
				}
				// Negative cases must catch every visit to gateway-1, even if
				// the client returns to gateway-0 before observation ends.
				since := time.Now().Add(-recentWindow)
				if !tc.rotate {
					since = started
				}
				var err error
				first, err = gatewayClients(probeCtx, k, ns.Name, "gateway-0", time.Now().Add(-recentWindow))
				if err != nil {
					return false, err
				}
				second, err = gatewayClients(probeCtx, k, ns.Name, "gateway-1", since)
				if err != nil {
					return false, err
				}
				if !tc.rotate {
					if len(second) > 0 {
						return false, fmt.Errorf("clients reached gateway-1 without rotation: %v", second)
					}
					return false, nil // Negative cases must observe the full minute.
				}
				for _, signal := range connectionSignals {
					_, newGatewayClients, total := connectionDestinations(first, second, signal)
					if newGatewayClients < 1 || total != clientCount {
						return false, nil
					}
				}
				return true, nil
			})
			t.Logf("delivery after scale-out (%s): %s", time.Since(started).Round(time.Second), connectionDeliverySummary(first, second, clientCount))
			if tc.rotate {
				require.NoError(t, err, "rotation must deliver each signal to the new gateway while all clients keep delivering")
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded, "the negative case must observe the full period")
				require.NoError(t, ctx.Err(), "the fixture deadline must not truncate the observation period")
				second, err = gatewayClients(ctx, k, ns.Name, "gateway-1", started)
				require.NoError(t, err)
				require.Empty(t, second)
				// Check fresh traffic too: historical logs alone could hide dead clients.
				first, err = gatewayClients(ctx, k, ns.Name, "gateway-0", time.Now().Add(-recentWindow))
				require.NoError(t, err)
				require.Len(t, first, expectedDeliveries, "every client must still deliver every signal to gateway-0")
			}
			require.NoError(t, waitConnectionPods(ctx, k, ns.Name, clientCount+gatewayCount, identities, reportPods))
			return featureCtx
		})
	}
	testenv.Test(t, f.Feature())
}

// Only fields inspected or changed by the test need Go types. Component
// configuration passed through unchanged remains opaque JSON.
type collectorSpec struct {
	Image  string          `json:"image"`
	Config collectorConfig `json:"config"`
}

type collectorConfig struct {
	Receivers collectorReceivers           `json:"receivers"`
	Exporters map[string]collectorExporter `json:"exporters"`
	Service   collectorService             `json:"service"`
}

type collectorReceivers struct {
	OTLP       *otlpReceiver   `json:"otlp,omitempty"`
	FileLog    json.RawMessage `json:"file_log,omitempty"`
	Prometheus json.RawMessage `json:"prometheus,omitempty"`
}

// Adapted from the Collector v0.162.0 OTLP receiver schema:
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/receiver/otlpreceiver/config.go
type otlpReceiver struct {
	Protocols map[string]otlpListener `json:"protocols"`
}

// Adapted from the Collector v0.162.0 gRPC and OTLP/HTTP receiver schemas:
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/config/configgrpc/generated_config.go
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/config/confighttp/server.go
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/receiver/otlpreceiver/config.go
// Strict decoding makes new fields require review here. Nested options that the
// fixture never changes remain intact as raw JSON for the Collector to validate.
type otlpListener struct {
	Endpoint             string          `json:"endpoint"`
	Transport            string          `json:"transport,omitempty"`
	TLS                  *collectorTLS   `json:"tls,omitempty"`
	Auth                 json.RawMessage `json:"auth,omitempty"`
	IncludeMetadata      json.RawMessage `json:"include_metadata,omitempty"`
	MaxConcurrentStreams json.RawMessage `json:"max_concurrent_streams,omitempty"`
	MaxRecvMsgSizeMiB    json.RawMessage `json:"max_recv_msg_size_mib,omitempty"`
	Middlewares          json.RawMessage `json:"middlewares,omitempty"`
	ReadBufferSize       json.RawMessage `json:"read_buffer_size,omitempty"`
	WriteBufferSize      json.RawMessage `json:"write_buffer_size,omitempty"`
	Keepalive            *struct {
		EnforcementPolicy json.RawMessage `json:"enforcement_policy,omitempty"`
		IdleTimeout       string          `json:"idle_timeout,omitempty"`
		ServerParameters  *struct {
			MaxConnectionAge      string `json:"max_connection_age,omitempty"`
			MaxConnectionAgeGrace string `json:"max_connection_age_grace,omitempty"`
			MaxConnectionIdle     string `json:"max_connection_idle,omitempty"`
			Time                  string `json:"time,omitempty"`
			Timeout               string `json:"timeout,omitempty"`
		} `json:"server_parameters,omitempty"`
	} `json:"keepalive,omitempty"`
	CORS                  json.RawMessage `json:"cors,omitempty"`
	MaxRequestBodySize    json.RawMessage `json:"max_request_body_size,omitempty"`
	ResponseHeaders       json.RawMessage `json:"response_headers,omitempty"`
	CompressionAlgorithms json.RawMessage `json:"compression_algorithms,omitempty"`
	ReadTimeout           json.RawMessage `json:"read_timeout,omitempty"`
	ReadHeaderTimeout     json.RawMessage `json:"read_header_timeout,omitempty"`
	WriteTimeout          json.RawMessage `json:"write_timeout,omitempty"`
	IdleTimeout           json.RawMessage `json:"idle_timeout,omitempty"`
	KeepAlivesEnabled     json.RawMessage `json:"keep_alives_enabled,omitempty"`
	TracesURLPath         json.RawMessage `json:"traces_url_path,omitempty"`
	MetricsURLPath        json.RawMessage `json:"metrics_url_path,omitempty"`
	LogsURLPath           json.RawMessage `json:"logs_url_path,omitempty"`
}

// Adapted from the Collector v0.162.0 OTLP exporter and gRPC client schemas:
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/otlpexporter/config.go
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/config/configgrpc/generated_config.go
// Debug exporter fields support fixture observation:
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/exporter/debugexporter/config.go
type collectorExporter struct {
	Endpoint           string          `json:"endpoint,omitempty"`
	TLS                *collectorTLS   `json:"tls,omitempty"`
	RetryOnFailure     json.RawMessage `json:"retry_on_failure,omitempty"`
	SendingQueue       json.RawMessage `json:"sending_queue,omitempty"`
	Timeout            json.RawMessage `json:"timeout,omitempty"`
	Auth               json.RawMessage `json:"auth,omitempty"`
	Authority          string          `json:"authority,omitempty"`
	BalancerName       json.RawMessage `json:"balancer_name,omitempty"`
	Compression        json.RawMessage `json:"compression,omitempty"`
	Headers            json.RawMessage `json:"headers,omitempty"`
	Keepalive          json.RawMessage `json:"keepalive,omitempty"`
	Middlewares        json.RawMessage `json:"middlewares,omitempty"`
	ReadBufferSize     json.RawMessage `json:"read_buffer_size,omitempty"`
	WriteBufferSize    json.RawMessage `json:"write_buffer_size,omitempty"`
	UserAgent          json.RawMessage `json:"user_agent,omitempty"`
	WaitForReady       json.RawMessage `json:"wait_for_ready,omitempty"`
	Verbosity          string          `json:"verbosity,omitempty"`
	SamplingInitial    int             `json:"sampling_initial,omitempty"`
	SamplingThereafter int             `json:"sampling_thereafter,omitempty"`
}

// Adapted from the Collector v0.162.0 client and server TLS schemas:
// https://github.com/open-telemetry/opentelemetry-collector/blob/v0.162.0/config/configtls/generated_config.go
type collectorTLS struct {
	CAFile                      string          `json:"ca_file,omitempty"`
	CAPEM                       string          `json:"ca_pem,omitempty"`
	CertFile                    string          `json:"cert_file,omitempty"`
	CertPEM                     string          `json:"cert_pem,omitempty"`
	KeyFile                     string          `json:"key_file,omitempty"`
	KeyPEM                      string          `json:"key_pem,omitempty"`
	ClientCAFile                string          `json:"client_ca_file,omitempty"`
	ServerNameOverride          string          `json:"server_name_override,omitempty"`
	CipherSuites                json.RawMessage `json:"cipher_suites,omitempty"`
	CurvePreferences            json.RawMessage `json:"curve_preferences,omitempty"`
	IncludeInsecureCipherSuites json.RawMessage `json:"include_insecure_cipher_suites,omitempty"`
	IncludeSystemCACertsPool    json.RawMessage `json:"include_system_ca_certs_pool,omitempty"`
	MaxVersion                  json.RawMessage `json:"max_version,omitempty"`
	MinVersion                  json.RawMessage `json:"min_version,omitempty"`
	ReloadInterval              json.RawMessage `json:"reload_interval,omitempty"`
	TPM                         json.RawMessage `json:"tpm,omitempty"`
	ReloadClientCAFile          json.RawMessage `json:"client_ca_file_reload,omitempty"`
	Insecure                    json.RawMessage `json:"insecure,omitempty"`
	InsecureSkipVerify          json.RawMessage `json:"insecure_skip_verify,omitempty"`
}

func (l *otlpListener) UnmarshalJSON(data []byte) error {
	type listener otlpListener
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*listener)(l)); err != nil {
		return fmt.Errorf("gateway listener configuration: %w", err)
	}
	return nil
}

func (e *collectorExporter) UnmarshalJSON(data []byte) error {
	type exporter collectorExporter
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*exporter)(e)); err != nil {
		return fmt.Errorf("Collector exporter configuration: %w", err)
	}
	return nil
}

func (r *otlpReceiver) UnmarshalJSON(data []byte) error {
	type receiver otlpReceiver
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*receiver)(r)); err != nil {
		return fmt.Errorf("OTLP receiver configuration: %w", err)
	}
	return nil
}

func TestConnectionConfigDecoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   string
		target any
	}{
		{name: "exporter", data: `{"endpoint":"gateway:4317","compression":"gzip","timeout":"3s","keepalive":{"time":"30s"},"sending_queue":{"batch":{"flush_timeout":"1s"}},"tls":{"min_version":"1.3","reload_interval":"2h"}}`, target: &collectorExporter{}},
		{name: "grpc_receiver", data: `{"protocols":{"grpc":{"endpoint":"0.0.0.0:14317","max_concurrent_streams":10,"tls":{"client_ca_file_reload":true},"keepalive":{"enforcement_policy":{"min_time":"1s"},"server_parameters":{"max_connection_age":"7m","max_connection_idle":"1m"}}}}}`, target: &otlpReceiver{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, json.Unmarshal([]byte(tc.data), tc.target))
			encoded, err := json.Marshal(tc.target)
			require.NoError(t, err)
			assert.JSONEq(t, tc.data, string(encoded), "connection options must survive decoding and encoding")
		})
	}
	for _, tc := range []struct {
		name   string
		data   string
		target any
	}{
		{name: "exporter", data: `{"future_option":true}`, target: &collectorExporter{}},
		{name: "tls", data: `{"tls":{"future_option":true}}`, target: &collectorExporter{}},
		{name: "receiver", data: `{"future_option":true}`, target: &otlpReceiver{}},
		{name: "listener", data: `{"protocols":{"grpc":{"future_option":true}}}`, target: &otlpReceiver{}},
		{name: "server_parameters", data: `{"keepalive":{"server_parameters":{"future_option":true}}}`, target: &otlpListener{}},
	} {
		t.Run("unknown_"+tc.name, func(t *testing.T) {
			require.ErrorContains(t, json.Unmarshal([]byte(tc.data), tc.target), `unknown field "future_option"`)
		})
	}
}

type collectorService struct {
	Pipelines map[string]collectorPipeline `json:"pipelines"`
	Telemetry json.RawMessage              `json:"telemetry,omitempty"`
}

type collectorPipeline struct {
	Receivers []string `json:"receivers"`
	Exporters []string `json:"exporters"`
}

func connectionPodTemplate(image, role string, port int32) corev1.PodTemplateSpec {
	template := passiveFixturePodTemplate("connections")
	client := role == "client"
	config := "gateway"
	if client {
		config, port = "clients", 0
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"role": role}}, Spec: template.Spec}
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	pod.Spec.SecurityContext.FSGroup = new(int64(65532))
	collector := pod.Spec.Containers[0].DeepCopy()
	collector.Name, collector.Image = "collector", image
	collector.Command = nil
	collector.Args = []string{"--config=/config/config.yaml"}
	collector.Env = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "192MiB"}}
	if client {
		collector.Env = append(collector.Env, corev1.EnvVar{Name: "CLIENT_ID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}})
	}
	collector.Resources = corev1.ResourceRequirements{
		// Each client emits once per second; separate Collector processes exercise
		// independent connections without reserving production throughput capacity.
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	collector.VolumeMounts = []corev1.VolumeMount{{Name: "config", MountPath: "/config", ReadOnly: true}}
	if port != 0 {
		collector.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		}, PeriodSeconds: 1}
	}
	pod.Spec.Volumes = []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: config}},
	}}}
	tlsRole := "server"
	if client {
		tlsRole = "client"
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "tls", VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: "tls-" + tlsRole},
	}})
	collector.VolumeMounts = append(collector.VolumeMounts, corev1.VolumeMount{
		Name: "tls", MountPath: "/etc/nktp/internal-tls/" + tlsRole, ReadOnly: true,
	})
	pod.Spec.Containers = []corev1.Container{*collector}
	if client {
		generator := template.Spec.Containers[0].DeepCopy()
		generator.Name = "generator"
		// OTLP/JSON format: https://opentelemetry.io/docs/specs/otlp/#otlphttp
		generator.Command = []string{"/bin/sh", "-ec", `
while true; do
  echo "rotation-logs-$CLIENT_ID" >> /logs/input.log
  now=$(date +%s)
  payload='{"resourceSpans":[{"resource":{"attributes":[{"key":"client_id","value":{"stringValue":"rotation-traces-'"$CLIENT_ID"'"}}]},"scopeSpans":[{"spans":[{"traceId":"0123456789abcdef0123456789abcdef","spanId":"0123456789abcdef","name":"connection-test","kind":1,"startTimeUnixNano":"'"${now}000000000"'","endTimeUnixNano":"'"${now}000000001"'"}]}]}]}'
  # The Collector may still be starting. Delivery assertions require successful posts.
  if wget -q -T 2 -O /dev/null --header 'Content-Type: application/json' --post-data "$payload" http://127.0.0.1:4319/v1/traces; then :; fi
  sleep 1
done`}
		generator.Env = []corev1.EnvVar{{Name: "CLIENT_ID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}}}
		generator.VolumeMounts = []corev1.VolumeMount{{Name: "logs", MountPath: "/logs"}}
		pod.Spec.Containers = append(pod.Spec.Containers, *generator)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "logs", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "logs", MountPath: "/logs", ReadOnly: true})
	}
	return corev1.PodTemplateSpec{ObjectMeta: pod.ObjectMeta, Spec: pod.Spec}
}

// Issue fixture credentials for its Service name; production certificates name
// the chart's gateway Services and cannot authenticate this isolated fixture.
func createConnectionTLS(ctx context.Context, t *testing.T, k kubernetes.Interface, ns string) {
	t.Helper()
	caPublic, caKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "connection-test-ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caKey)
	require.NoError(t, err)
	for i, role := range []string{"server", "client"} {
		public, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		cert := &x509.Certificate{
			SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: "connection-test-" + role},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		if role == "server" {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			cert.DNSNames = []string{"gateway." + ns + ".svc"}
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, ca, public, caKey)
		require.NoError(t, err)
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, err)
		_, err = k.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tls-" + role}, Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"ca.crt":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
				"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
				"tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "creating fixture %s credentials", role)
	}
}

func createConnectionGateway(ctx context.Context, t *testing.T, k kubernetes.Interface, ns, name, image string, port int32) {
	t.Helper()
	template := connectionPodTemplate(image, "gateway", port)
	pod := &corev1.Pod{ObjectMeta: template.ObjectMeta, Spec: template.Spec}
	pod.Name = name
	_, err := k.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	require.NoError(t, err, "creating %s", name)
}

func createConnectionClients(ctx context.Context, t *testing.T, k kubernetes.Interface, ns, image string, replicas int) {
	t.Helper()
	labels := map[string]string{"role": "client"}
	_, err := k.CoreV1().Services(ns).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "clients"},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Selector: labels},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "creating the client StatefulSet's headless Service")
	template := connectionPodTemplate(image, "client", 0)
	template.Spec.RestartPolicy = corev1.RestartPolicyAlways
	_, err = k.AppsV1().StatefulSets(ns).Create(ctx, &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "client"},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "clients", Replicas: new(int32(replicas)),
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: labels}, Template: template,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "creating %d Collector client replicas", replicas)
}

func waitConnectionPods(ctx context.Context, k kubernetes.Interface, ns string, count int, identities map[string]types.UID, report func([]corev1.Pod)) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := k.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		report(pods.Items)
		for _, pod := range pods.Items {
			if uid, known := identities[pod.Name]; known && uid != pod.UID {
				return false, fmt.Errorf("%s was replaced during the test", pod.Name)
			}
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				return false, fmt.Errorf("%s stopped: %s", pod.Name, pod.Status.Phase)
			}
			for _, status := range pod.Status.ContainerStatuses {
				if terminated := status.State.Terminated; terminated != nil {
					return false, fmt.Errorf("%s/%s exited with code %d: %s %s", pod.Name, status.Name,
						terminated.ExitCode, terminated.Reason, terminated.Message)
				}
				if status.RestartCount != 0 {
					return false, fmt.Errorf("%s/%s restarted", pod.Name, status.Name)
				}
				if !status.Ready {
					return false, nil
				}
			}
			if len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) {
				return false, nil
			}
		}
		return len(pods.Items) == count, nil
	})
}

func waitGatewayEndpoints(ctx context.Context, k kubernetes.Interface, ns string, count int) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		slices, err := k.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=gateway"})
		if err != nil {
			return false, err
		}
		ready := map[string]bool{}
		for _, slice := range slices.Items {
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready && endpoint.TargetRef != nil {
					ready[endpoint.TargetRef.Name] = true
				}
			}
		}
		return len(ready) == count, nil
	})
}

var connectionSignals = []string{"logs", "metrics", "traces"}

func logConnectionNodeCapacity(ctx context.Context, t *testing.T, k kubernetes.Interface) {
	nodes, err := k.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("reading node capacity: %v", err)
		return
	}
	for _, node := range nodes.Items {
		t.Logf("node %s: capacity=%v; allocatable=%v", node.Name, node.Status.Capacity, node.Status.Allocatable)
		for _, condition := range node.Status.Conditions {
			if condition.Status == corev1.ConditionTrue && condition.Type != corev1.NodeReady {
				t.Logf("node %s: %s=%s reason=%s message=%s", node.Name,
					condition.Type, condition.Status, condition.Reason, condition.Message)
			}
		}
	}
}

func connectionDeliverySummary(first, second map[string]time.Time, expected int) string {
	var summaries []string
	for _, signal := range connectionSignals {
		firstCount, secondCount, total := connectionDestinations(first, second, signal)
		var missing []string
		for i := range expected {
			id := fmt.Sprintf("rotation-%s-client-%d", signal, i)
			if _, found := first[id]; found {
				continue
			}
			if _, found := second[id]; !found {
				missing = append(missing, fmt.Sprintf("client-%d", i))
			}
		}
		summary := fmt.Sprintf("%s: gateway-0=%d gateway-1=%d delivering=%d/%d", signal, firstCount, secondCount, total, expected)
		if len(missing) > 0 {
			summary += fmt.Sprintf(" missing=%v", missing)
		}
		summaries = append(summaries, summary)
	}
	return strings.Join(summaries, "; ")
}

func logConnectionFailure(ctx context.Context, t *testing.T, k kubernetes.Interface, ns, stage string) {
	t.Logf("%s: failed during %q; fixture namespace=%s", t.Name(), stage, ns)
	pods, err := k.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("reading fixture pods: %v", err)
		return
	}
	for _, pod := range pods.Items {
		t.Logf("pod %s: phase=%s node=%s reason=%s message=%s", pod.Name,
			pod.Status.Phase, pod.Spec.NodeName, pod.Status.Reason, pod.Status.Message)
		for _, condition := range pod.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				t.Logf("pod %s: %s=%s reason=%s message=%s", pod.Name,
					condition.Type, condition.Status, condition.Reason, condition.Message)
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			t.Logf("container %s/%s: ready=%t restarts=%d state=%+v last termination=%+v",
				pod.Name, status.Name, status.Ready, status.RestartCount, status.State, status.LastTerminationState)
		}
		for _, container := range pod.Spec.Containers {
			body, err := k.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: container.Name, TailLines: new(int64(60)), LimitBytes: new(int64(16 << 10)),
			}).DoRaw(ctx)
			t.Logf("recent logs %s/%s (up to 60 lines/16 KiB): error=%v\n%s", pod.Name, container.Name, err, body)
		}
	}
	endpoints, err := k.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=gateway",
	})
	if err != nil {
		t.Logf("reading gateway endpoints: %v", err)
	} else {
		for _, slice := range endpoints.Items {
			for _, endpoint := range slice.Endpoints {
				t.Logf("gateway endpoint: target=%v addresses=%v conditions=%+v", endpoint.TargetRef, endpoint.Addresses, endpoint.Conditions)
			}
		}
	}
	events, err := k.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("reading fixture events: %v", err)
	} else {
		for _, event := range events.Items {
			if event.Type == corev1.EventTypeWarning {
				t.Logf("warning %s/%s: %s: %s", event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Reason, event.Message)
			}
		}
	}
}

// Use the latest receipt so a reconnect does not count a client at both gateways.
func connectionDestinations(first, second map[string]time.Time, signal string) (firstCount, secondCount, total int) {
	prefix := "rotation-" + signal + "-"
	for id, received := range first {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		total++
		if second[id].After(received) {
			secondCount++
		} else {
			firstCount++
		}
	}
	for id := range second {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if _, found := first[id]; found {
			continue
		}
		secondCount++
		total++
	}
	return firstCount, secondCount, total
}

var connectionClientID = regexp.MustCompile(`rotation-(logs|metrics|traces)-client-[0-9]\b`)

func gatewayClients(ctx context.Context, k kubernetes.Interface, ns, pod string, since time.Time) (map[string]time.Time, error) {
	body, err := k.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: "collector", Timestamps: true, SinceTime: &metav1.Time{Time: since}, LimitBytes: new(int64(4 << 20)),
	}).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	clients := map[string]time.Time{}
	for _, line := range strings.Split(string(body), "\n") {
		id := connectionClientID.FindString(line)
		if id == "" {
			continue
		}
		stamp, _, _ := strings.Cut(line, " ")
		received, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, fmt.Errorf("parsing %s log timestamp: %w", pod, err)
		}
		if !received.Before(since) && received.After(clients[id]) {
			clients[id] = received
		}
	}
	return clients, nil
}
