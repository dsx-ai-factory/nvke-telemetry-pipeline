// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build scale

package backendoutage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	otelv1 "github.com/open-telemetry/opentelemetry-operator/apis/v1beta1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	transportspdy "k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
)

// cluster handles Kubernetes resources, readiness, diagnostics, and
// streams in the explicitly selected cluster.
type cluster struct {
	core      coreclient.CoreV1Interface
	client    klient.Client
	config    *rest.Config
	namespace string
	output    string
}

func newCluster(kubeconfig, output string) (cluster, error) {
	// Load only the explicitly selected file, preserving its current context.
	credentials, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		return cluster{}, err
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*credentials, "", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return cluster{}, err
	}
	config.Timeout = 20 * time.Second
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		return cluster{}, err
	}
	client, err := klient.New(config)
	if err != nil {
		return cluster{}, err
	}
	if err := otelv1.AddToScheme(client.Resources().GetScheme()); err != nil {
		return cluster{}, err
	}
	return cluster{core: core, client: client, config: config, output: output}, nil
}

// operatorHealth requires an active Operator with a reconciled, ready
// rollout.
func operatorHealth(operators []appsv1.Deployment) error {
	if len(operators) == 0 {
		return fmt.Errorf("no OpenTelemetry Operator Deployment found")
	}
	for _, operator := range operators {
		replicas := int32(1)
		if operator.Spec.Replicas != nil {
			replicas = *operator.Spec.Replicas
		}
		status := operator.Status
		if operator.DeletionTimestamp != nil || replicas == 0 || status.ObservedGeneration < operator.Generation || status.UpdatedReplicas != replicas || status.ReadyReplicas < replicas || status.AvailableReplicas < replicas {
			return fmt.Errorf("OpenTelemetry Operator %s is unhealthy: desired=%d updated=%d ready=%d available=%d generation=%d observed=%d deleting=%t", operator.Name, replicas, status.UpdatedReplicas, status.ReadyReplicas, status.AvailableReplicas, operator.Generation, status.ObservedGeneration, operator.DeletionTimestamp != nil)
		}
	}
	return nil
}

func (c cluster) operatorPreflight(ctx context.Context, namespace string) error {
	c.namespace = namespace
	selector := "app.kubernetes.io/name=opentelemetry-operator"
	var operators appsv1.DeploymentList
	err := c.client.Resources(namespace).List(ctx, &operators, resources.WithLabelSelector(selector))
	if err == nil {
		err = operatorHealth(operators.Items)
	}
	if err == nil {
		return nil
	}
	data := c.diagnostics("", selector)
	path := filepath.Join(c.output, "diagnostics", namespace+"-operator-preflight.txt")
	if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
		return fmt.Errorf("NKTP preflight failed in namespace %s: %w; saving diagnostics: %v\n%s", namespace, err, writeErr, data)
	}
	return fmt.Errorf("NKTP preflight failed in namespace %s: %w\noperator diagnostics (%s):\n%s", namespace, err, path, data)
}

// waitDeployment waits for a complete one-replica rollout, including after
// restoration.
func (c cluster) waitDeployment(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Require one desired replica so restoration waits for the Operator to
	// reconcile the previously scaled-to-zero backend Deployment.
	return funcs.WaitForDeployment(ctx, funcs.DeploymentWaitOptions{
		Client: c.client, Namespace: c.namespace, Name: name,
		Rollout: true, ReplicaCount: new(int32(1)),
	})
}

func (c cluster) ready(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if err := c.waitDeployment(ctx, name+"-collector"); err != nil {
		data := c.diagnostics(name, "")
		path := filepath.Join(c.output, "diagnostics", name+"-startup.txt")
		if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
			t.Logf("saving startup diagnostics: %v", writeErr)
		}
		t.Fatalf("%v\nstartup diagnostics (%s):\n%s", err, path, data)
	}
}

// diagnostics bounds each API read separately, even after the test is
// canceled.
func (c cluster) diagnostics(collector, selector string) []byte {
	var output bytes.Buffer
	read := func(name string, get func(context.Context) (any, error)) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fmt.Fprintf(&output, "\n%s (namespace %s):\n", name, c.namespace)
		object, err := get(ctx)
		if err != nil {
			fmt.Fprintln(&output, err)
			return
		}
		if text, ok := object.([]byte); ok {
			output.Write(text)
			return
		}
		data, err := json.MarshalIndent(object, "", "  ")
		if err != nil {
			fmt.Fprintln(&output, err)
		} else {
			output.Write(data)
		}
	}
	if collector != "" {
		read("OpenTelemetryCollector "+collector, func(ctx context.Context) (any, error) {
			return c.getCollector(ctx, c.namespace, collector)
		})
	}
	for _, item := range []struct {
		name, selector string
		objects        k8s.ObjectList
	}{
		{"Deployments", selector, &appsv1.DeploymentList{}},
		{"ReplicaSets", selector, &appsv1.ReplicaSetList{}},
		{"Pods", selector, &corev1.PodList{}},
		{"Events", "", &corev1.EventList{}},
	} {
		read(item.name, func(ctx context.Context) (any, error) {
			err := c.client.Resources(c.namespace).List(ctx, item.objects, resources.WithLabelSelector(item.selector))
			return item.objects, err
		})
	}
	if collector == "" {
		read("Operator NetworkPolicy", func(ctx context.Context) (any, error) {
			var object networkingv1.NetworkPolicy
			err := c.client.Resources().Get(ctx, "opentelemetry-operator", c.namespace, &object)
			return &object, err
		})
	}
	deployment := ""
	if collector != "" {
		deployment = collector + "-collector"
	}
	for _, previous := range []bool{false, true} {
		read(fmt.Sprintf("Container logs (previous=%t)", previous), func(ctx context.Context) (any, error) {
			data, err := c.logs(ctx, deployment, selector, 60, previous)
			if collector != "" && !previous {
				_ = os.WriteFile(filepath.Join(c.output, "logs", collector+"-startup.log"), data, 0600)
			}
			return data, err
		})
	}
	return output.Bytes()
}

func (c cluster) deploymentPods(ctx context.Context, deployment string) (*corev1.PodList, error) {
	var object appsv1.Deployment
	err := c.client.Resources().Get(ctx, deployment, c.namespace, &object)
	if err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(object.Spec.Selector)
	if err != nil {
		return nil, err
	}
	var pods corev1.PodList
	err = c.client.Resources(c.namespace).List(ctx, &pods, resources.WithLabelSelector(selector.String()))
	return &pods, err
}

func (c cluster) runningPod(ctx context.Context, deployment string) (string, error) {
	pods, err := c.deploymentPods(ctx, deployment)
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					return pod.Name, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no ready Pod for Deployment %s/%s", c.namespace, deployment)
}

func (c cluster) logs(ctx context.Context, deployment, selector string, tail int64, previous bool) ([]byte, error) {
	var pods *corev1.PodList
	var err error
	if deployment != "" {
		pods, err = c.deploymentPods(ctx, deployment)
	} else {
		pods = &corev1.PodList{}
		err = c.client.Resources(c.namespace).List(ctx, pods, resources.WithLabelSelector(selector))
	}
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	for _, pod := range pods.Items {
		for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			fmt.Fprintf(&output, "\n[%s/%s previous=%t]\n", pod.Name, container.Name, previous)
			data, err := c.core.Pods(c.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name, TailLines: &tail, Previous: previous}).DoRaw(ctx)
			if err != nil {
				fmt.Fprintln(&output, err)
			} else {
				output.Write(data)
			}
		}
	}
	return output.Bytes(), nil
}

func (c cluster) getCollector(ctx context.Context, namespace, name string) (*otelv1.OpenTelemetryCollector, error) {
	var object otelv1.OpenTelemetryCollector
	err := c.client.Resources().Get(ctx, name, namespace, &object)
	return &object, err
}

func (c cluster) createCollector(ctx context.Context, source *otelv1.OpenTelemetryCollector) error {
	object := source.DeepCopy()
	object.Namespace = c.namespace
	return c.client.Resources(c.namespace).Create(ctx, object)
}

func (c cluster) scaleBackend(ctx context.Context, replicas int) error {
	object := &otelv1.OpenTelemetryCollector{ObjectMeta: metav1.ObjectMeta{Namespace: c.namespace, Name: "outage-blackhole"}}
	return c.client.Resources(c.namespace).Patch(ctx, object, k8s.Patch{PatchType: types.MergePatchType, Data: []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))})
}

func (c cluster) stopBackend(ctx context.Context) error {
	if err := c.scaleBackend(ctx, 0); err != nil {
		return err
	}
	return wait.For(func(ctx context.Context) (bool, error) {
		var pods corev1.PodList
		err := c.client.Resources(c.namespace).List(ctx, &pods, resources.WithLabelSelector("app.kubernetes.io/name=outage-blackhole-collector"))
		return len(pods.Items) == 0, err
	}, wait.WithContext(ctx), wait.WithTimeout(time.Minute), wait.WithInterval(time.Second), wait.WithImmediate())
}

func (c cluster) exec(ctx context.Context, deployment, container string, command ...string) ([]byte, error) {
	pod, err := c.runningPod(ctx, deployment)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	if err := c.client.Resources().ExecInPod(ctx, c.namespace, pod, container, command, &stdout, &stderr); err != nil {
		return stdout.Bytes(), fmt.Errorf("exec %s/%s: %w: %s", pod, container, err, &stderr)
	}
	return stdout.Bytes(), nil
}

func (c cluster) forward(t *testing.T, ctx context.Context, name string, ports ...int) map[int]string {
	t.Helper()
	pod, err := c.runningPod(ctx, name+"-collector")
	require.NoError(t, err)
	transport, upgrader, err := transportspdy.RoundTripperFor(c.config)
	require.NoError(t, err)
	request := c.core.RESTClient().Post().Resource("pods").Namespace(c.namespace).Name(pod).SubResource("portforward")
	dialer := transportspdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, request.URL())
	var bindings []string
	for _, port := range ports {
		bindings = append(bindings, ":"+strconv.Itoa(port))
	}
	file, err := os.CreateTemp(filepath.Join(c.output, "logs"), "forward-"+name+"-*.log")
	require.NoError(t, err)
	streamCtx, cancel := context.WithCancel(ctx)
	ready, done := make(chan struct{}), make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, bindings, streamCtx.Done(), ready, file, file)
	if err != nil {
		cancel()
		_ = file.Close()
		t.Fatal(err)
	}
	var forwardErr error
	go func() { defer close(done); forwardErr = forwarder.ForwardPorts() }()
	t.Cleanup(func() { cancel(); <-done; _ = file.Close() })
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
	case <-done:
		t.Fatalf("port-forward %s stopped: %v", name, forwardErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
		t.Fatalf("port-forward %s did not become ready", name)
	}
	bound, err := forwarder.GetPorts()
	require.NoError(t, err)
	endpoints := map[int]string{}
	for _, port := range bound {
		endpoints[int(port.Remote)] = "127.0.0.1:" + strconv.Itoa(int(port.Local))
	}
	return endpoints
}
