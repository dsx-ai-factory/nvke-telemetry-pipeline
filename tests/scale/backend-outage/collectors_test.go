// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	otelv1 "github.com/open-telemetry/opentelemetry-operator/apis/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// collectorOptions controls memory and sending-queue overrides for
// derived Collectors.
type collectorOptions struct {
	MemoryLimit  string
	QueueBytes   int64
	QueuePercent float64
}

type scraperOptions struct {
	collectorOptions
	RunID        string
	Interval     time.Duration
	Image        string
	DisableBatch bool
}

func readCollector(path string) (*otelv1.OpenTelemetryCollector, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var collector otelv1.OpenTelemetryCollector
	if err := yaml.Unmarshal(data, &collector); err != nil {
		return nil, err
	}
	return &collector, nil
}

// collectorImage uses the upstream GHCR publication for official Docker
// Hub Collector references, preserving versions and custom registry
// references.
func collectorImage(image string) string {
	for _, prefix := range []string{"otel/", "docker.io/otel/", "index.docker.io/otel/"} {
		if !strings.HasPrefix(image, prefix) {
			continue
		}
		name := strings.TrimPrefix(image, prefix)
		for _, distribution := range []string{"opentelemetry-collector-k8s", "opentelemetry-collector-contrib"} {
			if name == distribution || strings.HasPrefix(name, distribution+":") || strings.HasPrefix(name, distribution+"@") {
				return "ghcr.io/open-telemetry/opentelemetry-collector-releases/" + name
			}
		}
	}
	return image
}

// deriveBackend preserves the native verification pipeline and gives Go
// an 80% memory target, leaving room for serialization and non-heap
// allocations.
func deriveBackend(source *otelv1.OpenTelemetryCollector, memory string) (*otelv1.OpenTelemetryCollector, error) {
	if source.Kind != "OpenTelemetryCollector" || source.Name != "outage-blackhole" || source.Spec.Resources.Limits == nil {
		return nil, fmt.Errorf("manifest requires outage-blackhole Collector with resource limits")
	}
	out := source.DeepCopy()
	if err := memoryLimit(&out.Spec.Resources, memory); err != nil {
		return nil, err
	}
	limit := out.Spec.Resources.Limits.Memory().Value()
	value := strconv.FormatInt(int64(float64(limit)*.8), 10)
	for i := range out.Spec.Env {
		if out.Spec.Env[i].Name == "GOMEMLIMIT" {
			out.Spec.Env[i].Value, out.Spec.Env[i].ValueFrom = value, nil
			return out, nil
		}
	}
	out.Spec.Env = append(out.Spec.Env, corev1.EnvVar{Name: "GOMEMLIMIT", Value: value})
	return out, nil
}

// memoryLimit clamps the request when shrinking a container's limit.
func memoryLimit(resources *corev1.ResourceRequirements, override string) error {
	memory := resources.Limits.Memory().DeepCopy()
	if override != "" {
		var err error
		memory, err = resource.ParseQuantity(override)
		if err != nil {
			return fmt.Errorf("memory limit must be a positive Kubernetes quantity: %w", err)
		}
	}
	if memory.Sign() <= 0 {
		return fmt.Errorf("memory limit must be positive")
	}
	if resources.Limits == nil {
		resources.Limits = corev1.ResourceList{}
	}
	resources.Limits[corev1.ResourceMemory] = memory
	if resources.Requests.Memory().Cmp(memory) > 0 {
		resources.Requests[corev1.ResourceMemory] = memory
	}
	return nil
}

// component confines dynamic decoding to native component settings. The
// Operator API types cover the CR and pipelines, but components remain
// extensible.
func component(group *otelv1.AnyConfig, name string) (map[string]any, error) {
	if group != nil {
		if object, ok := group.Object[name].(map[string]any); ok {
			return object, nil
		}
	}
	return nil, fmt.Errorf("component %s absent or not an object", name)
}

func sendingQueue(collector *otelv1.OpenTelemetryCollector) (map[string]any, error) {
	exporter, err := component(&collector.Spec.Config.Exporters, "otlp")
	if err != nil {
		return nil, err
	}
	queue, ok := exporter["sending_queue"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("sending queue absent")
	}
	return queue, nil
}

// configureGatewaySpike applies an optional spike override and reports
// the effective percentage-based memory-limiter thresholds.
func configureGatewaySpike(collector *otelv1.OpenTelemetryCollector, override int) (map[string]float64, error) {
	if override < -1 || override == 0 || override > 100 {
		return nil, fmt.Errorf("gateway spike percentage must be -1 to inherit or a positive integer below the hard limit")
	}
	var limiter map[string]any
	if processors := collector.Spec.Config.Processors; processors != nil {
		for name := range processors.Object {
			if name == "memory_limiter" || strings.HasPrefix(name, "memory_limiter/") {
				if limiter != nil {
					return nil, fmt.Errorf("gateway spike setting requires a single memory limiter")
				}
				var err error
				limiter, err = component(processors, name)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	hard, _ := limiter["limit_percentage"].(float64)
	absolute, _ := limiter["limit_mib"].(float64)
	spikeAbsolute, _ := limiter["spike_limit_mib"].(float64)
	if hard <= 0 || absolute > 0 || spikeAbsolute > 0 {
		if override != -1 {
			return nil, fmt.Errorf("gateway spike override requires a percentage-based memory limiter")
		}
		return nil, nil
	}
	spike, _ := limiter["spike_limit_percentage"].(float64)
	if override != -1 {
		spike = float64(override)
		if spike >= hard {
			return nil, fmt.Errorf("gateway spike percentage must be below hard limit %.0f%%", hard)
		}
		limiter["spike_limit_percentage"] = spike
	} else if spike == 0 {
		spike = hard * .2
	}
	return map[string]float64{"hard_limit_percentage": hard, "spike_limit_percentage": spike, "soft_limit_percentage": hard - spike}, nil
}

// deriveGateway creates an isolated copy of the installed Collector.
func deriveGateway(source *otelv1.OpenTelemetryCollector, options collectorOptions) (*otelv1.OpenTelemetryCollector, error) {
	return deriveCollector(source, options, "outage-gateway", "otlp", "outage-blackhole-collector:4317")
}

// deriveCollector copies an installed Collector into a single-replica
// test Deployment, retaining processing settings and retargeting its
// transport.
func deriveCollector(source *otelv1.OpenTelemetryCollector, options collectorOptions, name, receiverName, endpoint string) (*otelv1.OpenTelemetryCollector, error) {
	if options.QueueBytes < 0 || math.IsNaN(options.QueuePercent) || options.QueuePercent < 0 || options.QueuePercent > 100 || (options.QueueBytes > 0 && options.QueuePercent > 0) {
		return nil, fmt.Errorf("queue bytes must be nonnegative and percentage in [0,100]; choose only one override")
	}
	derived := source.DeepCopy()
	spec := &derived.Spec
	sourceMemory := spec.Resources.Limits.Memory().Value()
	if sourceMemory <= 0 {
		return nil, fmt.Errorf("%s source: memory limit must be positive", name)
	}
	if _, exists := spec.Resources.Limits[corev1.ResourceCPU]; !exists {
		return nil, fmt.Errorf("%s source: CPU limit required", name)
	}
	if err := memoryLimit(&spec.Resources, options.MemoryLimit); err != nil {
		return nil, err
	}
	if err := scaleRuntime(spec, sourceMemory, options.MemoryLimit != ""); err != nil {
		return nil, err
	}
	config := &spec.Config
	if len(config.Exporters.Object) != 1 || len(config.Receivers.Object) != 1 || config.Service.Pipelines == nil {
		return nil, fmt.Errorf("%s source requires only otlp export, %s input, and service pipelines", name, receiverName)
	}
	if err := configureQueue(derived, options, sourceMemory); err != nil {
		return nil, err
	}
	// The isolated Deployment uses memory queues unless the test storage flag
	// attaches its own PVC; source claim templates and mounts are not copied.
	queue, err := sendingQueue(derived)
	if err != nil {
		return nil, err
	}
	if storage, ok := queue["storage"].(string); ok {
		delete(queue, "storage")
		delete(config.Extensions.Object, storage)
		config.Service.Extensions = slices.DeleteFunc(config.Service.Extensions, func(extension string) bool {
			return extension == storage
		})
	}
	if err := isolateTransport(config, receiverName, endpoint); err != nil {
		return nil, err
	}
	configureProfiling(config)
	return &otelv1.OpenTelemetryCollector{
		TypeMeta:   metav1.TypeMeta{APIVersion: otelv1.GroupVersion.String(), Kind: "OpenTelemetryCollector"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: otelv1.OpenTelemetryCollectorSpec{
			Mode: otelv1.ModeDeployment, Config: *config,
			OpenTelemetryCommonFields: otelv1.OpenTelemetryCommonFields{
				Image: collectorImage(spec.Image), Resources: spec.Resources, Env: spec.Env,
				NodeSelector: spec.NodeSelector, Tolerations: spec.Tolerations,
				Replicas: ptr.To(int32(1)), TerminationGracePeriodSeconds: ptr.To(int64(5)),
			},
		},
	}, nil
}

// scaleRuntime preserves the source GOMEMLIMIT-to-memory ratio when
// resizing and rejects environment references unavailable in the test
// namespace.
func scaleRuntime(spec *otelv1.OpenTelemetryCollectorSpec, sourceMemory int64, resize bool) error {
	if len(spec.EnvFrom) > 0 {
		return fmt.Errorf("envFrom cannot be resolved in isolated namespace")
	}
	for i := range spec.Env {
		entry := &spec.Env[i]
		if entry.ValueFrom != nil && (entry.ValueFrom.SecretKeyRef != nil || entry.ValueFrom.ConfigMapKeyRef != nil) {
			return fmt.Errorf("external environment references cannot be isolated")
		}
		if resize && entry.Name == "GOMEMLIMIT" && entry.Value != "off" {
			limit, err := resource.ParseQuantity(strings.TrimSuffix(entry.Value, "B"))
			if err != nil || limit.Sign() <= 0 || entry.ValueFrom != nil {
				return fmt.Errorf("memory override requires a literal GOMEMLIMIT byte quantity or off")
			}
			entry.Value = strconv.FormatInt(int64(float64(limit.Value())*float64(spec.Resources.Limits.Memory().Value())/float64(sourceMemory)), 10)
		}
	}
	if resize && spec.Config.Processors != nil {
		for name := range spec.Config.Processors.Object {
			if name == "memory_limiter" || strings.HasPrefix(name, "memory_limiter/") {
				limiter, err := component(spec.Config.Processors, name)
				if err != nil {
					return err
				}
				for _, key := range []string{"limit_mib", "spike_limit_mib"} {
					if limit, _ := limiter[key].(float64); limit > 0 {
						return fmt.Errorf("memory override requires a percentage-based memory limiter")
					}
				}
			}
		}
	}
	return nil
}

// configureQueue applies a byte or percentage override, otherwise
// preserving the source queue-to-memory ratio when resizing.
func configureQueue(collector *otelv1.OpenTelemetryCollector, options collectorOptions, sourceMemory int64) error {
	queue, err := sendingQueue(collector)
	if err != nil {
		return err
	}
	if options.QueueBytes == 0 && options.QueuePercent == 0 && options.MemoryLimit == "" {
		return nil
	}
	if queue["sizer"] != "bytes" {
		return fmt.Errorf("queue override requires bytes sizer")
	}
	capacity := options.QueueBytes
	memory := collector.Spec.Resources.Limits.Memory().Value()
	if capacity == 0 {
		if options.QueuePercent > 0 {
			capacity = int64(float64(memory) * options.QueuePercent / 100)
		} else {
			oldCapacity, _ := queue["queue_size"].(float64)
			capacity = int64(oldCapacity * float64(memory) / float64(sourceMemory))
		}
		if capacity <= 0 {
			return fmt.Errorf("derived queue capacity must be at least one byte")
		}
	}
	queue["queue_size"] = float64(capacity)
	return nil
}

// isolateTransport redirects OTLP export within the test namespace and
// removes inbound TLS, rejecting source authentication settings.
func isolateTransport(config *otelv1.Config, receiverName, endpoint string) error {
	exporter, err := component(&config.Exporters, "otlp")
	if err != nil {
		return err
	}
	if exporter["auth"] != nil || exporter["headers"] != nil {
		return fmt.Errorf("authenticated exporter cannot be copied into an isolated test")
	}
	receiver, err := component(&config.Receivers, receiverName)
	if err != nil {
		return err
	}
	if receiverName == "otlp" {
		protocols, _ := receiver["protocols"].(map[string]any)
		grpc, ok := protocols["grpc"].(map[string]any)
		if !ok {
			return fmt.Errorf("OTLP/gRPC receiver absent")
		}
		if grpc["auth"] != nil {
			return fmt.Errorf("authenticated receiver cannot be isolated")
		}
		delete(grpc, "tls")
		grpc["endpoint"] = "0.0.0.0:4317"
	}
	exporter["endpoint"], exporter["tls"] = endpoint, map[string]any{"insecure": true}
	return nil
}

func configureProfiling(config *otelv1.Config) {
	if config.Extensions == nil {
		config.Extensions = &otelv1.AnyConfig{}
	}
	if config.Extensions.Object == nil {
		config.Extensions.Object = map[string]any{}
	}
	config.Extensions.Object["pprof"] = map[string]any{"endpoint": "127.0.0.1:1777"}
	if !slices.Contains(config.Service.Extensions, "pprof") {
		config.Service.Extensions = append(config.Service.Extensions, "pprof")
	}
	config.Service.Telemetry = &otelv1.AnyConfig{Object: map[string]any{
		"metrics": map[string]any{"level": "detailed", "readers": []any{map[string]any{"pull": map[string]any{"exporter": map[string]any{"prometheus": map[string]any{"host": "0.0.0.0", "port": float64(8888)}}}}}},
		"traces":  map[string]any{"level": "none"},
	}}
}

// deriveScraper retargets the installed scraper to the test payload and
// adds delivery-verification instrumentation, applying requested overrides.
func deriveScraper(source, instrumentation *otelv1.OpenTelemetryCollector, options scraperOptions) (*otelv1.OpenTelemetryCollector, error) {
	if options.Interval <= 0 {
		return nil, fmt.Errorf("scrape interval must be positive")
	}
	out, err := deriveCollector(source, options.collectorOptions, "outage-scraper", "prometheus", "outage-gateway-collector:4317")
	if err != nil {
		return nil, err
	}
	if options.Image != "" {
		out.Spec.Image = options.Image
	} else {
		out.Spec.Image = strings.Replace(out.Spec.Image, "/opentelemetry-collector-k8s:", "/opentelemetry-collector-contrib:", 1)
	}
	for _, entry := range out.Spec.Env {
		if entry.Name == "OUTAGE_RUN_ID" {
			return nil, fmt.Errorf("source uses reserved OUTAGE_RUN_ID environment variable")
		}
	}
	out.Spec.Env = append(out.Spec.Env, corev1.EnvVar{Name: "OUTAGE_RUN_ID", Value: options.RunID})
	if err := configureScrape(&out.Spec.Config, &instrumentation.Spec.Config, options.Interval); err != nil {
		return nil, err
	}
	if err := instrumentScraper(&out.Spec.Config, &instrumentation.Spec.Config, options.DisableBatch); err != nil {
		return nil, err
	}
	return out, nil
}

// configureScrape replaces source targets with the test job while
// retaining the source Prometheus receiver settings.
func configureScrape(config, instrumentation *otelv1.Config, interval time.Duration) error {
	receiver, err := component(&config.Receivers, "prometheus")
	if err != nil {
		return err
	}
	delete(receiver, "target_allocator")
	scrapeConfig, ok := receiver["config"].(map[string]any)
	if !ok {
		return fmt.Errorf("source Prometheus scrape config absent")
	}
	testReceiver, err := component(&instrumentation.Receivers, "prometheus")
	if err != nil {
		return err
	}
	// Copy instrumentation as well as the source; callers can reuse both inputs.
	jobConfig := (&otelv1.AnyConfig{Object: testReceiver}).DeepCopy().Object
	jobs := jobConfig["config"].(map[string]any)["scrape_configs"].([]any)
	job := jobs[0].(map[string]any)
	job["scrape_interval"], job["scrape_timeout"] = interval.String(), min(interval, 10*time.Second).String()
	scrapeConfig["scrape_configs"] = []any{job}
	return nil
}

// instrumentScraper adds scrape receipt counters and identifiers,
// optionally removing batch processors while preserving exporter queue
// batching.
func instrumentScraper(config, instrumentation *otelv1.Config, disableBatch bool) error {
	if config.Processors == nil {
		config.Processors = &otelv1.AnyConfig{Object: map[string]any{}}
	}
	for name, value := range instrumentation.Processors.DeepCopy().Object {
		if _, exists := config.Processors.Object[name]; exists {
			return fmt.Errorf("source uses reserved processor %s", name)
		}
		config.Processors.Object[name] = value
	}
	config.Connectors = instrumentation.Connectors.DeepCopy()
	config.Exporters.Object["prometheus"] = instrumentation.Exporters.DeepCopy().Object["prometheus"]
	pipeline := config.Service.Pipelines["metrics"]
	if pipeline == nil || len(config.Service.Pipelines) != 1 {
		return fmt.Errorf("source requires only the metrics pipeline")
	}
	var processors []string
	for _, name := range pipeline.Processors {
		if disableBatch && (name == "batch" || strings.HasPrefix(name, "batch/")) {
			delete(config.Processors.Object, name)
		} else {
			processors = append(processors, name)
		}
	}
	pipeline.Processors = append(append([]string{"filter/scrape"}, processors...), "transform/identify")
	pipeline.Exporters = []string{"otlp", "count/scrapes"}
	config.Service.Pipelines["metrics/scrapes"] = instrumentation.Service.Pipelines["metrics/scrapes"].DeepCopy()
	return nil
}

// testManifest supplies a small installed Collector independent of
// chart defaults. Expected manifests below describe the isolation changes in
// full.
func testManifest(t *testing.T, receiver string) *otelv1.OpenTelemetryCollector {
	t.Helper()
	data := fmt.Sprintf(`spec:
  image: example/collector:tag
  replicas: 2
  additionalContainers: [{name: sidecar}]
  resources:
    limits: {memory: 8Gi, cpu: "4"}
    requests: {memory: 2Gi, cpu: 200m}
  env: [{name: GOMEMLIMIT, value: 6GiB}, {name: GOGC, value: "50"}]
  config:
    extensions:
      file_storage/queue: {directory: /production/queue}
    receivers: {%s}
    processors:
      memory_limiter: {limit_percentage: 80, spike_limit_percentage: 25}
      batch: {send_batch_size: 100}
    exporters:
      otlp:
        endpoint: production:4317
        sending_queue: {storage: file_storage/queue, sizer: bytes, queue_size: 16777216, block_on_overflow: true, num_consumers: 32, batch: {flush_timeout: 1s}}
        retry_on_failure: {enabled: true}
    service:
      extensions: [file_storage/queue]
      pipelines:
        metrics: {receivers: [%s], processors: [memory_limiter, batch], exporters: [otlp]}
`, receiver, strings.SplitN(receiver, ":", 2)[0])
	var out otelv1.OpenTelemetryCollector
	require.NoError(t, yaml.Unmarshal([]byte(data), &out))
	return &out
}

type collectorExpectation struct {
	memory, request, runtime, image string
	queue                           int64
	spike                           int
}

func testExpected(t *testing.T, name, receiver, destination string, want collectorExpectation) *otelv1.OpenTelemetryCollector {
	t.Helper()
	var expected otelv1.OpenTelemetryCollector
	require.NoError(t, yaml.Unmarshal([]byte(fmt.Sprintf(`apiVersion: opentelemetry.io/v1beta1
kind: OpenTelemetryCollector
metadata: {name: %s}
spec:
  mode: deployment
  replicas: 1
  terminationGracePeriodSeconds: 5
  image: %s
  resources:
    limits: {memory: %s, cpu: "4"}
    requests: {memory: %s, cpu: 200m}
  env: [{name: GOMEMLIMIT, value: "%s"}, {name: GOGC, value: "50"}]
  config:
    receivers: {%s}
    processors:
      memory_limiter: {limit_percentage: 80, spike_limit_percentage: %d}
      batch: {send_batch_size: 100}
    exporters:
      otlp:
        endpoint: %s
        tls: {insecure: true}
        sending_queue: {sizer: bytes, queue_size: %d, block_on_overflow: true, num_consumers: 32, batch: {flush_timeout: 1s}}
        retry_on_failure: {enabled: true}
    extensions:
      pprof: {endpoint: "127.0.0.1:1777"}
    service:
      extensions: [pprof]
      telemetry:
        metrics:
          level: detailed
          readers: [{pull: {exporter: {prometheus: {host: 0.0.0.0, port: 8888}}}}]
        traces: {level: none}
      pipelines:
        metrics: {receivers: [%s], processors: [memory_limiter, batch], exporters: [otlp]}
`, name, want.image, want.memory, want.request, want.runtime, receiver, want.spike, destination, want.queue, strings.SplitN(receiver, ":", 2)[0])), &expected))
	return &expected
}

func TestGatewayDerivation(t *testing.T) {
	source := testManifest(t, `otlp: {protocols: {grpc: {tls: {cert_file: /production/tls.crt}, max_recv_msg_size_mib: 8}}}`)
	original := source.DeepCopy()
	for _, test := range []struct {
		name, memory                         string
		bytes                                int64
		percent                              float64
		spike                                int
		wantMemory, wantRequest, wantRuntime string
		wantQueue                            int64
		wantSpike                            int
	}{
		{"inherited", "", 0, 0, -1, "8Gi", "2Gi", "6GiB", 16 << 20, 25},
		{"scaled ratio", "1Gi", 0, 0, -1, "1Gi", "1Gi", "805306368", 2 << 20, 25},
		{"combined overrides", "1Gi", 0, 25, 16, "1Gi", "1Gi", "805306368", 256 << 20, 16},
		{"absolute queue", "", 64 << 20, 0, -1, "8Gi", "2Gi", "6GiB", 64 << 20, 25},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := deriveGateway(source, collectorOptions{MemoryLimit: test.memory, QueueBytes: test.bytes, QueuePercent: test.percent})
			require.NoError(t, err)
			settings, err := configureGatewaySpike(actual, test.spike)
			require.NoError(t, err)
			expected := testExpected(t, "outage-gateway", `otlp: {protocols: {grpc: {endpoint: "0.0.0.0:4317", max_recv_msg_size_mib: 8}}}`, "outage-blackhole-collector:4317", collectorExpectation{
				memory: test.wantMemory, request: test.wantRequest, runtime: test.wantRuntime,
				image: "example/collector:tag", queue: test.wantQueue, spike: test.wantSpike,
			})
			assert.Equal(t, expected, actual)
			assert.Equal(t, map[string]float64{"hard_limit_percentage": 80, "spike_limit_percentage": float64(test.wantSpike), "soft_limit_percentage": float64(80 - test.wantSpike)}, settings)
		})
	}
	assert.Equal(t, original, source, "derivation must not mutate the installed Collector")
}

func TestScraperDerivation(t *testing.T) {
	source := testManifest(t, `prometheus: {target_allocator: {endpoint: production}, config: {global: {sample_limit: 200000}, scrape_configs: [{job_name: production}]}}`)
	instrumentation, err := readCollector("scraper.yaml")
	require.NoError(t, err)
	testConfig := instrumentation.Spec.Config
	for _, test := range []struct {
		name, memory, image                             string
		percent                                         float64
		disableBatch                                    bool
		wantMemory, wantRequest, wantRuntime, wantImage string
		wantQueue                                       int64
	}{
		{"inherited", "", "", 0, false, "8Gi", "2Gi", "6GiB", "example/collector:tag", 16 << 20},
		{"combined overrides", "1Gi", "example/custom:tag", 25, true, "1Gi", "1Gi", "805306368", "example/custom:tag", 256 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := deriveScraper(source, instrumentation, scraperOptions{
				collectorOptions: collectorOptions{MemoryLimit: test.memory, QueuePercent: test.percent},
				RunID:            "test-run", Interval: 2 * time.Second, Image: test.image, DisableBatch: test.disableBatch,
			})
			require.NoError(t, err)
			expected := testExpected(t, "outage-scraper", `prometheus: {config: {global: {sample_limit: 200000}, scrape_configs: [{job_name: outage-scrape-server, scrape_interval: 2s, scrape_timeout: 2s, fallback_scrape_protocol: PrometheusText0.0.4, static_configs: [{targets: ["outage-scrape-server:8080"]}]}]}}`, "outage-gateway-collector:4317", collectorExpectation{
				memory: test.wantMemory, request: test.wantRequest, runtime: test.wantRuntime,
				image: test.wantImage, queue: test.wantQueue, spike: 25,
			})
			expected.Spec.Env = append(expected.Spec.Env, corev1.EnvVar{Name: "OUTAGE_RUN_ID", Value: "test-run"})
			config := &expected.Spec.Config
			for name, value := range testConfig.Processors.Object {
				config.Processors.Object[name] = value
			}
			config.Connectors = testConfig.Connectors
			config.Exporters.Object["prometheus"] = testConfig.Exporters.Object["prometheus"]
			processors := []string{"filter/scrape", "memory_limiter", "batch", "transform/identify"}
			if test.disableBatch {
				delete(config.Processors.Object, "batch")
				processors = []string{"filter/scrape", "memory_limiter", "transform/identify"}
			}
			config.Service.Pipelines["metrics"] = &otelv1.Pipeline{Receivers: []string{"prometheus"}, Processors: processors, Exporters: []string{"otlp", "count/scrapes"}}
			config.Service.Pipelines["metrics/scrapes"] = &otelv1.Pipeline{Receivers: []string{"count/scrapes"}, Processors: []string{"transform/scrape_receipts", "deltatocumulative/scrapes"}, Exporters: []string{"prometheus"}}
			assert.Equal(t, expected, actual)
		})
	}
}

func TestBackendDerivation(t *testing.T) {
	source, err := readCollector("collectors.yaml")
	require.NoError(t, err)
	actual, err := deriveBackend(source, "64Mi")
	require.NoError(t, err)
	expected := source.DeepCopy()
	expected.Spec.Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
	expected.Spec.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("64Mi")
	expected.Spec.Env = []corev1.EnvVar{{Name: "GOGC", Value: "50"}, {Name: "GOMEMLIMIT", Value: "53687091"}}
	assert.Equal(t, expected, actual, "resizing must preserve the verification pipeline")
}
