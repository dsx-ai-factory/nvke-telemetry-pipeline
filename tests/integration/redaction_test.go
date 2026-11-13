// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type object = map[string]any

func TestTelemetryRedaction(t *testing.T) {
	image, config := renderedCollector(t.Context(), t)
	// Use local HTTP transport while preserving the rendered processor chains.
	config["receivers"] = object{"otlp": object{
		"protocols": object{"http": object{"endpoint": "0.0.0.0:4318"}},
	}}
	service := config["service"].(object)
	// The capture exporter disables queueing, so storage extensions are unused.
	delete(config, "extensions")
	delete(service, "extensions")
	for _, signal := range []string{"logs", "metrics", "traces"} {
		pipeline := service["pipelines"].(object)[signal].(object)
		pipeline["receivers"] = []string{"otlp"}
		pipeline["exporters"] = []string{"otlp_http"}
	}
	// Self-telemetry routing is checked in the chart tests.
	service["telemetry"] = object{"metrics": object{"level": "none"}}
	exporters, received := captureExports(t)
	config["exporters"] = exporters
	url := runCollector(t, image, config)
	for _, signal := range []string{"logs", "metrics", "traces"} {
		t.Run(signal, func(t *testing.T) {
			var input, expected object
			if signal == "logs" {
				input, expected = redactionFixture(t)
			} else {
				input, expected = metadataFixture(t, signal)
			}
			// The watchdog's cluster processor adds the default name to every resource.
			for _, resources := range expected {
				for _, entry := range resources.([]any) {
					resource := entry.(object)["resource"].(object)
					attributes, _ := resource["attributes"].([]any)
					resource["attributes"] = append(attributes, attribute("k8s.cluster.name", value("stringValue", "unknown")))
				}
			}
			// Source and gateway redaction must preserve already-masked telemetry.
			for hop := 0; hop < 2; hop++ {
				sendTelemetry(t, url+"/v1/"+signal, input)
				exported := receiveTelemetry(t, received)
				require.Equal(t, normalize(expected), normalize(exported))
				require.NotContains(t, string(marshal(t, exported)), "FAKE_")
				input = exported
			}
		})
	}
}

func renderedCollector(ctx context.Context, t *testing.T) (string, object) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		collectorUnavailable(t, "redaction integration test requires Docker")
	}
	dockerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(dockerCtx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		collectorUnavailable(t, "Docker is unavailable: %s", output)
	}
	var collector struct {
		Spec struct {
			Image          string
			Config         object
			InitContainers []struct {
				Args []string
			}
		}
	}
	rendered := command(t, "helm", "template", "redaction", "../../nktp", "--show-only", "templates/watchdog.yaml")
	require.NoError(t, yaml.Unmarshal(rendered, &collector))
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(inspectCtx, "docker", "image", "inspect", collector.Spec.Image).Run(); err != nil {
		require.NoError(t, inspectCtx.Err(), "inspecting Collector image %q", collector.Spec.Image)
		collectorUnavailable(t, "Collector image unavailable. Run docker pull %s before this test", collector.Spec.Image)
	}
	// Each workload repeats these rules in its native defaults.
	reference := collector.Spec.Config["processors"].(object)
	t.Run("watchdog-sidecar", func(t *testing.T) {
		require.Len(t, collector.Spec.InitContainers, 1)
		args := collector.Spec.InitContainers[0].Args
		require.Len(t, args, 1)
		var sidecar object
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(args[0], "--config=yaml:")), &sidecar))
		processors := sidecar["processors"].(object)
		require.Equal(t, reference["redaction/basic_v1"], processors["redaction/basic_v1"])
		service := sidecar["service"].(object)
		for _, signal := range []string{"logs", "metrics", "traces"} {
			pipeline := service["pipelines"].(object)[signal].(object)
			require.Contains(t, pipeline["processors"], "redaction/basic_v1", "sidecar %s pipeline", signal)
		}
		require.Equal(t, "none", service["telemetry"].(object)["traces"].(object)["level"])
		command(t, "docker", "run", "--rm", "--pull=never", collector.Spec.Image, "validate", args[0])
	})
	for _, workload := range []string{
		"daemonset",
		"cluster-events",
		"gateway-logs",
		"gateway-metrics",
		"gateway-traces",
		"large-target-metrics",
		"nodeless-metrics",
	} {
		t.Run(workload, func(t *testing.T) {
			var other struct {
				Spec struct{ Config struct{ Processors object } }
			}
			rendered := command(t, "helm", "template", "redaction", "../../nktp", "--show-only", "templates/"+workload+".yaml")
			require.NoError(t, yaml.Unmarshal(rendered, &other))
			processors := other.Spec.Config.Processors
			require.Equal(t, reference["redaction/basic_v1"], processors["redaction/basic_v1"])
		})
	}
	return collector.Spec.Image, collector.Spec.Config
}

func collectorUnavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("NKTP_REQUIRE_COLLECTOR") == "true" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func metadataFixture(t *testing.T, signal string) (object, object) {
	t.Helper()
	build := func(redact bool) object {
		resource := object{"attributes": redactionAttributes("resource", redact)}
		scope := object{"name": "inference-test", "version": "1", "attributes": redactionAttributes("scope", redact)}
		const timestamp = "1700000000000000000"
		const traceID = "0102030405060708090a0b0c0d0e0f10"
		const spanID = "0102030405060708"
		if signal == "traces" {
			span := object{
				"traceId": traceID, "spanId": spanID, "name": "model.inference", "kind": 2,
				"startTimeUnixNano": timestamp, "endTimeUnixNano": "1700000000000000100",
				"attributes": redactionAttributes("span", redact), "status": object{"code": 1},
				"events": []any{
					object{"name": "inference.started", "timeUnixNano": timestamp, "attributes": redactionAttributes("event", redact)},
					object{"name": "clean-event", "timeUnixNano": timestamp},
				},
				// Link attributes are outside the pinned redaction processor's supported surfaces.
				"links": []any{object{"traceId": traceID, "spanId": spanID, "attributes": []any{attribute("password", value("stringValue", "outside-scope"))}}},
			}
			return object{"resourceSpans": []any{object{"resource": resource, "scopeSpans": []any{object{"scope": scope, "spans": []any{span}}}}}}
		}
		var metrics []any
		for _, kind := range []string{"gauge", "sum", "histogram", "exponentialHistogram", "summary"} {
			point := object{"timeUnixNano": timestamp, "startTimeUnixNano": "1699999999999999900", "attributes": redactionAttributes("datapoint", redact)}
			data := object{"dataPoints": []any{point}}
			switch kind {
			case "gauge":
				point["asDouble"] = 12.5
			case "sum":
				point["asInt"] = "123"
				data["aggregationTemporality"] = 2
				data["isMonotonic"] = true
			case "histogram":
				point["count"], point["sum"] = "3", 12.5
				point["bucketCounts"], point["explicitBounds"] = []any{"1", "2"}, []any{5.0}
				data["aggregationTemporality"] = 2
			case "exponentialHistogram":
				point["scale"] = 1
				point["positive"] = object{"offset": 1, "bucketCounts": []any{"1", "2"}}
				point["negative"] = object{"offset": 1, "bucketCounts": []any{"1"}}
				point["count"], point["sum"] = "4", 10.5
				data["aggregationTemporality"] = 2
			case "summary":
				point["count"], point["sum"] = "3", 12.5
				point["quantileValues"] = []any{object{"quantile": 0.5, "value": 4.0}}
			}
			metrics = append(metrics, object{"name": "inference." + kind, "description": "Inference measurement", "unit": "1", kind: data})
		}
		// KSM Secret names identify separate series and must survive every hop.
		var points []any
		for _, secret := range []string{"asdf", "another-secret"} {
			points = append(points, object{
				"timeUnixNano": timestamp, "asDouble": 1,
				"attributes": []any{
					attribute("secret", value("stringValue", secret)),
					attribute("namespace", value("stringValue", "test")),
				},
			})
		}
		metrics = append(metrics, object{"name": "kube_secret_info", "gauge": object{"dataPoints": points}})
		// automount_token describes ServiceAccount configuration, not a credential.
		var accounts []any
		for i, automount := range []string{"true", "false", ""} {
			accounts = append(accounts, object{
				"timeUnixNano": timestamp, "asDouble": 1,
				"attributes": []any{
					attribute("namespace", value("stringValue", "test")),
					attribute("serviceaccount", value("stringValue", fmt.Sprintf("test-account-%d", i))),
					attribute("uid", value("stringValue", fmt.Sprintf("test-account-uid-%d", i))),
					attribute("automount_token", value("stringValue", automount)),
				},
			})
		}
		metrics = append(metrics, object{"name": "kube_serviceaccount_info", "gauge": object{"dataPoints": accounts}})
		return object{"resourceMetrics": []any{object{"resource": resource, "scopeMetrics": []any{object{"scope": scope, "metrics": metrics}}}}}
	}
	var input, expected object
	require.NoError(t, json.Unmarshal(marshal(t, build(false)), &input))
	require.NoError(t, json.Unmarshal(marshal(t, build(true)), &expected))
	return input, expected
}

func captureExports(t *testing.T) (object, <-chan object) {
	t.Helper()
	received := make(chan object, 8)
	sink := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload object
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	require.NoError(t, sink.Listener.Close())
	sink.Listener = listener
	sink.Start()
	t.Cleanup(sink.Close)
	endpoint := fmt.Sprintf("http://host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
	// HTTP/JSON lets the test inspect redacted payloads with the local HTTP sink.
	exporters := object{"otlp_http": object{
		"endpoint": endpoint, "encoding": "json", "compression": "none",
		"sending_queue": object{"enabled": false}, "retry_on_failure": object{"enabled": false},
	}}
	return exporters, received
}

func redactionAttributes(surface string, redact bool) []any {
	secretValues := []any{
		value("stringValue", "FAKE_PASSWORD"), value("stringValue", ""), object{},
		value("intValue", "12345"), value("doubleValue", 1.25), value("boolValue", true),
		value("bytesValue", "RkFLRV9CWVRFUw=="),
		value("arrayValue", object{"values": []any{value("stringValue", "FAKE_ARRAY")}}),
		value("kvlistValue", object{"values": []any{attribute("nested", value("stringValue", "FAKE_MAP"))}}),
		value("stringValue", "FAKE_COOKIE_VALUE"),
	}
	keys := []string{
		"PASSWORD", "passwd", "secret", "token", "api.key", "api-key", "api_key", "private.key",
		"http.request.header.authorization", "cookie", "access_token", "refresh_token", "id_token",
		"session_token", "auth_token", "accessToken", "refreshToken", "clientSecret", "secret_key", "set-cookie",
		"dbPassword", "userPassword", "apiToken", "bearerToken", "githubToken", "AWS_SECRET_ACCESS_KEY",
		"x-auth-token", "x-amz-security-token", "client_secret_key", "dbPASSWORD", "appApiKey",
	}
	result := []any{attribute("count", value("intValue", "7")), attribute("ok", value("boolValue", true)), attribute("message", value("stringValue", "****")),
		attribute("automount_token", value("stringValue", "false"))}
	// The exception is exact: a prefixed key still follows the blocking rules.
	automount := "true"
	if redact {
		automount = "****"
	}
	result = append(result, attribute(surface+".automount_token", value("stringValue", automount)))
	// Inference usage and configuration fields must keep their original numeric types.
	for _, key := range []string{"token_count", "tokenCount", "access_token_count", "refreshTokenCount", "input_tokens", "output_tokens", "total_tokens", "max_tokens", "gen_ai.usage.input_tokens", "token.count", "tokenizer", "secret_count", "password_length"} {
		result = append(result, attribute(key, value("intValue", "123")))
	}
	result = append(result, attribute("tokens_per_second", value("doubleValue", 12.5)))
	for i, text := range redactionTexts(redact) {
		result = append(result, attribute(fmt.Sprintf("text.%d", i), value("stringValue", text)))
	}
	for i, key := range keys {
		v := secretValues[i%len(secretValues)]
		exactValue := v
		if key == "secret" {
			// Ignored keys bypass value scanning too, for every signal and surface.
			exactValue = value("stringValue", "password=reference-only")
		}
		if redact {
			v = value("stringValue", "****")
			if key != "secret" {
				exactValue = v
			}
		}
		result = append(result, attribute(key, exactValue), attribute(surface+"."+key, v))
	}
	return result
}

func redactionTexts(redact bool) []string {
	fixtures := []struct {
		input string
		want  string
	}{
		{
			input: `{"token":"FAKE_JSON","authToken":"FAKE_AUTH_JSON","token_count":123}`,
			want:  `{****,****,"token_count":123}`,
		},
		{
			input: `{"password":"FAKE_\"QUOTED SECRET","ok":true}`,
			want:  `{****,"ok":true}`,
		},
		{input: "password='FAKE_SPACED SECRET' token_count=123", want: "**** token_count=123"},
		{input: "Authorization: Basic FAKE_BASIC==", want: "****"},
		{input: "Authorization: bEaReR FAKE_BEARER==", want: "****"},
		{input: "https://example.test/?token=FAKE_URL", want: "https://example.test/?****"},
		{input: "basic configuration token_count=123", want: "basic configuration token_count=123"},
		// Structured-key exceptions do not exempt assignments inside free text.
		{input: "secret=FAKE_TEXT automount_token=true", want: "**** ****"},
	}
	var texts []string
	for _, fixture := range fixtures {
		text := fixture.input
		if redact {
			text = fixture.want
		}
		texts = append(texts, text)
	}
	return texts
}

func redactionFixture(t *testing.T) (object, object) {
	t.Helper()
	build := func(redact bool) object {
		text := "PASSWORD = FAKE_ASSIGN, token:FAKE_TOKEN; Authorization: bEaReR FAKE_BEARER== api-key=FAKE_API access_token=FAKE_ACCESS refreshToken=FAKE_REFRESH auth.token=FAKE_AUTH db_password=FAKE_DB dbPassword=FAKE_CAMEL apiToken=FAKE_API_TOKEN bearerToken=FAKE_BEARER_TOKEN AWS_SECRET_ACCESS_KEY=FAKE_AWS x-auth-token=FAKE_HEADER"
		if redact {
			text = "****, ****; **** **** **** **** **** **** **** **** **** **** ****"
		}
		text += " token_count=123 input_tokens=100 output_tokens=23 token.count=123 tokens_per_second=12.5"
		resource := object{"attributes": append(redactionAttributes("resource", redact), attribute("k8s.pod.name", value("stringValue", "test-pod")), attribute("nvidia.telemetry.class", value("stringValue", "k8s.audit")))}
		scope := object{"name": "redaction-test", "version": "1", "attributes": redactionAttributes("scope", redact)}
		body := append(redactionAttributes("body", redact), attribute("auditID", value("stringValue", "test-audit")),
			attribute("verb", value("stringValue", "get")), attribute("stage", value("stringValue", "ResponseComplete")),
			attribute("user", value("kvlistValue", object{"values": []any{attribute("username", value("stringValue", "test-actor"))}})),
			attribute("objectRef", value("kvlistValue", object{"values": []any{attribute("resource", value("stringValue", "pods"))}})),
			attribute("responseStatus", value("kvlistValue", object{"values": []any{attribute("code", value("intValue", "200"))}})))
		nestedText, nestedSecret := "password=FAKE_NESTED_TEXT", "FAKE_NESTED_PASSWORD"
		if redact {
			nestedText, nestedSecret = "****", "****"
		}
		records := []any{
			object{"body": value("kvlistValue", object{"values": body}), "attributes": redactionAttributes("log", redact),
				"timeUnixNano": "1700000000000000000", "observedTimeUnixNano": "1700000000000000001",
				"severityNumber": 9, "severityText": "INFO", "traceId": "0102030405060708090a0b0c0d0e0f10", "spanId": "0102030405060708"},
			object{"body": value("stringValue", text)},
			object{"body": value("stringValue", "ordinary log")},
			object{"body": value("intValue", "17")}, object{"body": object{}},
			// Native masking also visits nested bodies and text inside map values.
			object{"body": value("kvlistValue", object{"values": []any{
				attribute("MESSAGE", value("stringValue", nestedText)),
				attribute("nested", value("kvlistValue", object{"values": []any{
					attribute("password", value("stringValue", nestedSecret)),
					attribute("secret", value("stringValue", "password=reference-only")),
					attribute("automount_token", value("stringValue", "true")),
				}})),
			}})},
		}
		for _, text := range redactionTexts(redact) {
			records = append(records, object{"body": value("stringValue", text)})
		}
		result := logs(resource, scope, records)
		// A clean resource after a secret-bearing resource detects scratch data leaking between records.
		result["resourceLogs"] = append(result["resourceLogs"].([]any), object{"resource": object{}, "scopeLogs": []any{object{"scope": object{}, "logRecords": []any{object{"body": value("stringValue", "clean")}}}}})
		return result
	}
	// Round-trip to give numbers and maps the same representation as exported JSON.
	var input, expected object
	require.NoError(t, json.Unmarshal(marshal(t, build(false)), &input))
	require.NoError(t, json.Unmarshal(marshal(t, build(true)), &expected))
	return input, expected
}

func logs(resource, scope object, records []any) object {
	return object{"resourceLogs": []any{object{"resource": resource, "scopeLogs": []any{object{"scope": scope, "logRecords": records}}}}}
}

func value(kind string, data any) object    { return object{kind: data} }
func attribute(key string, data any) object { return object{"key": key, "value": data} }

func normalize(v any) any {
	switch v := v.(type) {
	case object:
		for key, child := range v {
			v[key] = normalize(child)
		}
	case []any:
		for i, child := range v {
			v[i] = normalize(child)
		}
		if len(v) > 0 {
			if first, ok := v[0].(object); ok && first["key"] != nil {
				sort.Slice(v, func(i, j int) bool { return v[i].(object)["key"].(string) < v[j].(object)["key"].(string) })
			}
		}
	}
	return v
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func command(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	require.NoError(t, err, "%s %v: %s", name, args, output)
	return output
}

func runCollector(t *testing.T, image string, config object) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	data, err := yaml.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0644))
	mount := path + ":/etc/otelcol/config.yaml:ro"
	command(t, "docker", "run", "--rm", "--pull=never", "-v", mount, image, "validate", "--config=/etc/otelcol/config.yaml")
	args := []string{"run", "-d", "--rm", "--pull=never", "-p", "127.0.0.1::4318", "-v", mount}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host=host.docker.internal:host-gateway")
	}
	args = append(args, image, "--config=/etc/otelcol/config.yaml")
	id := strings.TrimSpace(string(command(t, "docker", args...)))
	t.Cleanup(func() {
		defer command(t, "docker", "stop", "--time=1", id)
		diagnostics := command(t, "docker", "logs", id)
		require.NotContains(t, string(diagnostics), "FAKE_", "Collector diagnostics must not expose fixture credentials")
	})
	address := strings.TrimSpace(string(command(t, "docker", "port", id, "4318/tcp")))
	require.Eventually(t, func() bool {
		client := &http.Client{Timeout: 200 * time.Millisecond}
		response, err := client.Get("http://" + address + "/")
		if err != nil {
			return false
		}
		return assert.NoError(t, response.Body.Close()) && response.StatusCode == http.StatusNotFound
	}, 10*time.Second, 50*time.Millisecond, "Collector did not start")
	return "http://" + address
}

func sendTelemetry(t *testing.T, url string, payload object) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(url, "application/json", bytes.NewReader(marshal(t, payload)))
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, response.Body.Close())
	}()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
}

func receiveTelemetry(t *testing.T, ch <-chan object) object {
	t.Helper()
	select {
	case payload := <-ch:
		return payload
	case <-time.After(5 * time.Second):
		t.Fatal("Collector did not export OTLP")
		return nil
	}
}
