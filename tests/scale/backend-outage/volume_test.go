// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/backend-outage/pkg/outagereport"
	otelv1 "github.com/open-telemetry/opentelemetry-operator/apis/v1beta1"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const queueDirectory = "/var/lib/otelcol/queue"
const queueDataDirectory = queueDirectory + "/data"

// configureGatewayStorage attaches one dedicated PVC to the isolated gateway.
// The observer only reads file metadata; its container I/O is excluded from
// gateway cAdvisor counters. Namespace cleanup removes the claim.
func configureGatewayStorage(gateway *otelv1.OpenTelemetryCollector, size, class string) (*corev1.PersistentVolumeClaim, error) {
	quantity, err := resource.ParseQuantity(size)
	if err != nil || quantity.Sign() <= 0 {
		return nil, fmt.Errorf("gateway storage must be a positive Kubernetes quantity, got %q", size)
	}
	queue, err := sendingQueue(gateway)
	if err != nil {
		return nil, err
	}
	queue["storage"] = "file_storage/outage"
	config := &gateway.Spec.Config
	config.Extensions.Object["file_storage/outage"] = map[string]any{"directory": queueDataDirectory, "create_directory": true}
	config.Service.Extensions = append(config.Service.Extensions, "file_storage/outage")
	gateway.Spec.PodSecurityContext = &corev1.PodSecurityContext{FSGroup: ptr.To(int64(10001)), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch)}
	gateway.Spec.Volumes = append(gateway.Spec.Volumes, corev1.Volume{
		Name: "queue", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "outage-queue"}},
	})
	gateway.Spec.VolumeMounts = append(gateway.Spec.VolumeMounts, corev1.VolumeMount{Name: "queue", MountPath: queueDirectory})
	gateway.Spec.AdditionalContainers = append(gateway.Spec.AdditionalContainers, corev1.Container{
		Name: "queue-observer", Image: "public.ecr.aws/docker/library/busybox:1.37.0",
		Command:      []string{"sleep", "2147483647"},
		VolumeMounts: []corev1.VolumeMount{{Name: "queue", MountPath: queueDirectory, ReadOnly: true}},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: ptr.To(int64(10001)), RunAsGroup: ptr.To(int64(10001)), RunAsNonRoot: ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("8Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
		},
	})
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "outage-queue"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: quantity}},
		},
	}
	if class != "" {
		claim.Spec.StorageClassName = ptr.To(class)
	}
	return claim, nil
}

// volumeMonitor samples only the selected gateway container and queue directory.
// Samples retain errors rather than converting absent metrics into zeros.
type volumeMonitor struct {
	cluster cluster
	pod     corev1.Pod
	claim   *corev1.PersistentVolumeClaim
	samples []outagereport.VolumeSample
}

func newVolumeMonitor(ctx context.Context, c cluster, claim *corev1.PersistentVolumeClaim) (*volumeMonitor, error) {
	name, err := c.runningPod(ctx, "outage-gateway-collector")
	if err != nil {
		return nil, err
	}
	var pod corev1.Pod
	if err := c.client.Resources(c.namespace).Get(ctx, name, c.namespace, &pod); err != nil {
		return nil, err
	}
	var bound corev1.PersistentVolumeClaim
	if err := c.client.Resources(c.namespace).Get(ctx, claim.Name, c.namespace, &bound); err != nil {
		return nil, err
	}
	return &volumeMonitor{cluster: c, pod: pod, claim: &bound}, nil
}

func (d *volumeMonitor) sample(ctx context.Context, phase string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s := outagereport.VolumeSample{Time: time.Now(), Phase: phase}
	// Pin exec to the same Pod used for cAdvisor attribution, even after a
	// rollout. A replacement must not silently reset the sampled counters.
	var stdout, stderr bytes.Buffer
	err := d.cluster.client.Resources().ExecInPod(ctx, d.cluster.namespace, d.pod.Name, "queue-observer",
		[]string{"sh", "-c", "du -sk " + queueDataDirectory + " && du -sb " + queueDataDirectory}, &stdout, &stderr)
	if err != nil {
		s.UsageError = fmt.Sprintf("queue directory usage: %v: %s", err, &stderr)
	} else {
		s.AllocatedBytes, s.ApparentBytes, err = parseQueueUsage(stdout.String())
		if err != nil {
			s.UsageError = err.Error()
		}
	}
	data, err := d.cluster.core.RESTClient().Get().Resource("nodes").Name(d.pod.Spec.NodeName).
		SubResource("proxy").Suffix("metrics/cadvisor").DoRaw(ctx)
	if err != nil {
		s.IOError = fmt.Sprintf("kubelet cAdvisor: %v", err)
	} else {
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, parseErr := parser.TextToMetricFamilies(bytes.NewReader(data))
		if parseErr != nil {
			s.IOError = fmt.Sprintf("cAdvisor parse: %v", parseErr)
		} else {
			s.Devices, err = gatewayVolumeCounters(families, d.cluster.namespace, d.pod.Name, s.Time)
			if err != nil {
				s.IOError = err.Error()
			}
		}
	}
	if ctx.Err() != context.Canceled {
		d.samples = append(d.samples, s)
	}
}

func (d *volumeMonitor) report() *outagereport.VolumeReport {
	r := outagereport.SummarizeVolume(d.samples)
	r.Pod, r.Node, r.PVC, r.PV = d.pod.Name, d.pod.Spec.NodeName, d.claim.Name, d.claim.Spec.VolumeName
	r.RequestedBytes = float64(d.claim.Spec.Resources.Requests.Storage().Value())
	if d.claim.Spec.StorageClassName != nil {
		r.StorageClass = *d.claim.Spec.StorageClassName
	}
	return r
}

func parseQueueUsage(text string) (*float64, *float64, error) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 {
		return nil, nil, fmt.Errorf("expected allocated and apparent queue directory sizes, got %q", text)
	}
	values := [2]float64{}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != queueDataDirectory {
			return nil, nil, fmt.Errorf("unexpected du output %q", line)
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid du size: %w", err)
		}
		values[i] = float64(n)
	}
	values[0] *= 1024 // du -k counts allocated 1KiB blocks, not file lengths.
	return &values[0], &values[1], nil
}

// gatewayVolumeCounters rejects aggregates and other containers, retaining
// device series separately to avoid double-counting stacked block devices.
func gatewayVolumeCounters(families map[string]*dto.MetricFamily, namespace, pod string, observed time.Time) (map[string]outagereport.VolumeCounters, error) {
	names := []string{"container_fs_reads_bytes_total", "container_fs_writes_bytes_total", "container_fs_reads_total", "container_fs_writes_total"}
	devices := map[string]outagereport.VolumeCounters{}
	for _, name := range names {
		family := families[name]
		if family == nil || family.GetType() != dto.MetricType_COUNTER {
			return nil, fmt.Errorf("cAdvisor counter %s unavailable", name)
		}
		found := false
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["namespace"] != namespace || labels["pod"] != pod || labels["container"] != "otc-container" {
				continue
			}
			device := labels["device"]
			value := metric.GetCounter().GetValue()
			if device == "" || metric.Counter == nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("invalid gateway cAdvisor series %s", name)
			}
			c := devices[device]
			if c.Values == nil {
				c = outagereport.VolumeCounters{Time: observed, Values: map[string]float64{}}
				if metric.TimestampMs != nil {
					c.Time = time.UnixMilli(metric.GetTimestampMs())
				}
			}
			if _, duplicate := c.Values[name]; duplicate {
				return nil, fmt.Errorf("ambiguous gateway cAdvisor series %s device %s", name, device)
			}
			c.Values[name] = value
			devices[device] = c
			found = true
		}
		if !found {
			return nil, fmt.Errorf("no cAdvisor %s series for %s/%s otc-container", name, namespace, pod)
		}
	}
	for device, counters := range devices {
		if len(counters.Values) != len(names) {
			return nil, fmt.Errorf("incomplete gateway cAdvisor counters for device %s", device)
		}
	}
	return devices, nil
}

func TestGatewayVolumeAttribution(t *testing.T) {
	names := []string{"container_fs_reads_bytes_total", "container_fs_writes_bytes_total", "container_fs_reads_total", "container_fs_writes_total"}
	var text strings.Builder
	for _, name := range names {
		fmt.Fprintf(&text, "# TYPE %s counter\n", name)
		for _, series := range []struct {
			namespace, pod, container, device string
			value                             int
		}{
			{"test", "gateway", "otc-container", "/dev/sda", 10},
			{"test", "gateway", "queue-observer", "/dev/sda", 100},
			{"test", "gateway-other", "otc-container", "/dev/sda", 1000},
			{"other", "gateway", "otc-container", "/dev/sda", 10000},
			{"test", "gateway", "", "/dev/sda", 100000},
			{"test", "gateway", "otc-container", "/dev/dm-0", 20},
		} {
			fmt.Fprintf(&text, "%s{namespace=%q,pod=%q,container=%q,device=%q} %d 1000\n", name, series.namespace, series.pod, series.container, series.device, series.value)
		}
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(text.String()))
	require.NoError(t, err)
	devices, err := gatewayVolumeCounters(families, "test", "gateway", time.Now())
	require.NoError(t, err)
	require.Len(t, devices, 2)
	for _, name := range names {
		assert.Equal(t, float64(10), devices["/dev/sda"].Values[name])
		assert.Equal(t, float64(20), devices["/dev/dm-0"].Values[name])
	}
	assert.Equal(t, time.UnixMilli(1000), devices["/dev/sda"].Time)
}
