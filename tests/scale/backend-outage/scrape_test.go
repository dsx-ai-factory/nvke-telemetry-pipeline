// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
)

// enableScraping creates one token per targeted scrape before exposing the
// phase to HTTP requests. Pausing hides the remaining tokens.
const enableScraping = `mkdir /scrapes/next
for i in $(seq 1 "$1"); do
  touch "/scrapes/next/$i"
done
mv /scrapes/next /scrapes/available`

func (r *run) setScraping(active bool) {
	r.t.Helper()
	command := `paused=$(mktemp -d /scrapes/paused.XXXXXX)
mv /scrapes/available "$paused/"`
	args := []string{}
	if active {
		command = enableScraping
		args = []string{strconv.Itoa(r.scrapeTarget)}
	}
	_, err := r.cluster.exec(r.ctx, "outage-scrape-server", "http", append([]string{"sh", "-ec", command, "sh"}, args...)...)
	require.NoError(r.t, err)
}

// scrapePayload repeats the template samples for fake pods with unique
// pod names and UIDs, spread across 100 node names. Each template repetition
// shares a pod identity; series is the total number of samples, not the pod
// count. The HTTP Pod serves the same series on every scrape: there is no pod
// churn creating new label sets over time, unlike real pod replacements.
func scrapePayload(templatePath, destination string, series int) (int64, error) {
	if series <= 0 {
		return 0, fmt.Errorf("scrape series must be positive")
	}
	template, err := os.ReadFile(templatePath)
	if err != nil {
		return 0, err
	}
	var headers, samples []string
	for _, line := range strings.Split(string(template), "\n") {
		if strings.HasPrefix(line, "#") {
			headers = append(headers, line)
		} else if line != "" {
			samples = append(samples, line)
		}
	}
	if len(samples) == 0 {
		return 0, fmt.Errorf("scrape has no samples")
	}
	file, err := os.Create(destination)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	w := bufio.NewWriter(file)
	for _, line := range headers {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return 0, err
		}
	}
	for i := range series {
		pod := i / len(samples)
		line := strings.ReplaceAll(samples[i%len(samples)], "__SERIES__", fmt.Sprintf("%06d", pod))
		line = strings.ReplaceAll(line, "__NODE__", fmt.Sprintf("%03d", pod%100))
		if _, err := fmt.Fprintln(w, line); err != nil {
			return 0, err
		}
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}
	return stat.Size(), file.Close()
}

// scrapeSeed compresses the payload into chunks below the ConfigMap
// size limit; the server's init container joins and decompresses them onto
// emptyDir.
func scrapeSeed(path string) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.Copy(writer, file); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	var chunks [][]byte
	data := compressed.Bytes()
	for len(data) > 0 {
		size := min(len(data), 512*1024)
		chunks = append(chunks, data[:size])
		data = data[size:]
	}
	return chunks, nil
}

func (c cluster) installScrapeServer(ctx context.Context, path string) error {
	chunks, err := scrapeSeed(path)
	if err != nil {
		return err
	}
	var projected []corev1.VolumeProjection
	for i, chunk := range chunks {
		name := fmt.Sprintf("outage-scrape-server-%06d", i)
		key := fmt.Sprintf("%06d", i)
		object := &corev1.ConfigMap{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.namespace},
			BinaryData: map[string][]byte{key: chunk},
		}
		data, err := json.MarshalIndent(object, "", "  ")
		if err != nil {
			return err
		}
		artifact := filepath.Join(c.output, "manifests", name+"-configmap.json")
		if err := os.WriteFile(artifact, data, 0600); err != nil {
			return err
		}
		// Create directly without a last-applied annotation duplicating the data.
		if err := c.client.Resources(c.namespace).Create(ctx, object); err != nil {
			return err
		}
		projected = append(projected, corev1.VolumeProjection{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: name}}})
	}
	objects, err := decoder.DecodeAllFiles(ctx, os.DirFS("."), "scrape-server.yaml", decoder.MutateNamespace(c.namespace))
	if err != nil {
		return err
	}
	if len(objects) != 2 {
		return fmt.Errorf("scrape manifest must contain Deployment and Service")
	}
	for _, object := range objects {
		var name string
		switch object := object.(type) {
		case *appsv1.Deployment:
			name = "scrape-server-deployment.json"
			for i := range object.Spec.Template.Spec.Volumes {
				volume := &object.Spec.Template.Spec.Volumes[i]
				if volume.Name == "seed" {
					volume.Projected = &corev1.ProjectedVolumeSource{Sources: projected}
				}
			}
		case *corev1.Service:
			name = "scrape-server-service.json"
		default:
			return fmt.Errorf("unexpected scrape server object %T", object)
		}
		data, err := json.MarshalIndent(object, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(c.output, "manifests", name), data, 0600); err != nil {
			return err
		}
		if err := c.client.Resources(c.namespace).Create(ctx, object); err != nil {
			return err
		}
	}
	if err := c.waitDeployment(ctx, "outage-scrape-server"); err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	size, err := c.exec(ctx, "outage-scrape-server", "http", "sh", "-c", "wc -c < /scrapes/metrics.paused")
	if err != nil {
		return err
	}
	got, err := strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)
	if err != nil || got != stat.Size() {
		return fmt.Errorf("scrape size=%q, expected %d bytes", size, stat.Size())
	}
	return nil
}

// TestScrapeLimit exercises the payload server's script with a two-scrape budget.
func TestScrapeLimit(t *testing.T) {
	root := t.TempDir()
	scrapes, seed := filepath.Join(root, "scrapes"), filepath.Join(root, "seed")
	require.NoError(t, os.MkdirAll(scrapes, 0755))
	require.NoError(t, os.MkdirAll(seed, 0755))
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err := writer.Write([]byte("test_metric 1\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(filepath.Join(seed, "000000"), compressed.Bytes(), 0600))
	objects, err := decoder.DecodeAllFiles(t.Context(), os.DirFS("."), "scrape-server.yaml")
	require.NoError(t, err)
	deployment := objects[0].(*appsv1.Deployment)
	init := deployment.Spec.Template.Spec.InitContainers[0].Command[2]
	init = strings.ReplaceAll(strings.ReplaceAll(init, "/scrapes", scrapes), "/seed/", seed+"/")
	output, err := exec.CommandContext(t.Context(), "sh", "-ec", init).CombinedOutput()
	require.NoError(t, err, "%s", output)
	enable := strings.ReplaceAll(enableScraping, "/scrapes", scrapes)
	output, err = exec.CommandContext(t.Context(), "sh", "-ec", enable, "sh", "2").CombinedOutput()
	require.NoError(t, err, "%s", output)
	for i := range 3 {
		output, err := exec.CommandContext(t.Context(), "sh", filepath.Join(scrapes, "cgi-bin", "metrics")).CombinedOutput()
		require.NoError(t, err, "%s", output)
		if i < 2 {
			assert.Contains(t, string(output), "test_metric 1\n")
		} else {
			assert.Contains(t, string(output), "503 Service Unavailable")
			assert.NotContains(t, string(output), "test_metric")
		}
	}
}

func TestScrapePayload(t *testing.T) {
	for _, test := range []struct {
		template      string
		labels, bytes int
	}{
		{"metrics-ksm.prom", 5, 192},
		{"metrics-dcgm.prom", 10, 384},
		{"metrics-ksm-mixed.prom", 10, 192},
	} {
		t.Run(test.template, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metrics.prom")
			_, err := scrapePayload(test.template, path, 7)
			require.NoError(t, err)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "__SERIES__")
			assert.NotContains(t, string(data), "__NODE__")
			families := testMetrics(t, string(data))
			require.Len(t, families, 1)
			seen := map[string]bool{}
			for _, family := range families {
				require.Len(t, family.Metric, 7)
				for _, metric := range family.Metric {
					assert.Len(t, metric.Label, test.labels)
					key := metric.String()
					assert.False(t, seen[key], "duplicate series: %s", key)
					seen[key] = true
				}
			}
			for _, line := range strings.Split(string(data), "\n") {
				if line != "" && !strings.HasPrefix(line, "#") {
					assert.Len(t, line, test.bytes)
				}
			}
		})
	}
}
