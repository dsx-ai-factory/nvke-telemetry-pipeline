// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"go.yaml.in/yaml/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	netutil "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	appsclient "k8s.io/client-go/kubernetes/typed/apps/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	// statefulSetPollInterval spaces readiness and deletion observations.
	statefulSetPollInterval = time.Second
	// statefulSetReadAttempts bounds consecutive transient read failures.
	statefulSetReadAttempts = 3
	// statefulSetCleanupTimeout bounds cleanup after the run context expires.
	statefulSetCleanupTimeout = 5 * time.Minute
)

// StatefulSetGenerator runs long-lived Pods with stable replica names.
// Construct it with NewStatefulSetGenerator.
type StatefulSetGenerator struct {
	// kubeconfig selects the credential file shared by the plan.
	kubeconfig string
	// config holds the native Pod template and workload lifetime.
	config statefulSetConfig
}

// statefulSetConfig describes StatefulSets and their run duration.
type statefulSetConfig struct {
	Count int `json:"count"`
	// Replicas is the number of Pods maintained by the StatefulSet.
	Replicas int32 `json:"replicas"`
	// Duration starts when every replica first becomes Ready.
	Duration metav1.Duration `json:"duration"`
	// PodTemplate supplies native container, metadata and scheduling settings.
	PodTemplate corev1.PodTemplateSpec `json:"podTemplate"`
}

// NewStatefulSetGenerator parses a native Pod template without accessing a cluster.
// Count and replicas default to one. Run defaults the namespace to default.
// A zero count or zero replicas creates nothing.
// Duration must be positive. Kubernetes validates submitted Pod fields.
func NewStatefulSetGenerator(spec yaml.Node, kubeconfig string) (*StatefulSetGenerator, error) {
	cfg := statefulSetConfig{Count: 1, Replicas: 1}
	if err := decodeSpec(spec, &cfg); err != nil {
		return nil, fmt.Errorf("decode statefulset spec: %w", err)
	}
	if cfg.Count < 0 {
		return nil, errors.New("count must not be negative")
	}
	if cfg.Replicas < 0 {
		return nil, errors.New("replicas must not be negative")
	}
	if cfg.Duration.Duration <= 0 {
		return nil, errors.New("duration must be positive")
	}
	return &StatefulSetGenerator{kubeconfig: kubeconfig, config: cfg}, nil
}

// Run creates StatefulSets with headless Services and observes their replicas.
// Each StatefulSet runs for Duration after its replicas first become Ready and
// must be Ready again before completion, allowing replacement during the run.
// Cancellation and API failures stop the run. Cleanup removes owned resources
// and has its own five-minute timeout. Run returns both run and cleanup errors.
func (g *StatefulSetGenerator) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return lifecycle.RunError(ctx, ctx.Err())
	}
	if g.config.Count == 0 || g.config.Replicas == 0 {
		return nil
	}
	// credentials selects the supplied file's current context.
	credentials, err := clientcmd.LoadFromFile(g.kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	// config bounds API requests independently of the overall plan deadline.
	config, err := clientcmd.NewNonInteractiveClientConfig(*credentials, "", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	config.Timeout = 30 * time.Second
	config.QPS, config.Burst = -1, -1
	// apps uses the real typed client for controller operations.
	apps, err := appsclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create StatefulSet client: %w", err)
	}
	// core creates the governing Service in the same namespace.
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Service client: %w", err)
	}
	return g.run(ctx, apps, core)
}

// run requires the cancellation, count and replica checks performed by Run and typed
// clients configured for the target cluster.
func (g *StatefulSetGenerator) run(ctx context.Context, apps appsclient.AppsV1Interface, core coreclient.CoreV1Interface) error {
	return runInstances(ctx, g.config.Count, func(ctx context.Context) error {
		return g.runStatefulSet(ctx, apps, core)
	})
}

// runStatefulSet owns one StatefulSet and retains cleanup failures after a requested stop.
func (g *StatefulSetGenerator) runStatefulSet(ctx context.Context, apps appsclient.AppsV1Interface, core coreclient.CoreV1Interface) (err error) {
	// workload carries ownership before Create so a lost response can be cleaned up.
	workload := newStatefulSet(g.config)
	// sets confines controller operations to the template's namespace.
	sets := apps.StatefulSets(workload.Namespace)
	defer func() {
		// cleanupCtx remains usable after cancellation of the plan.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statefulSetCleanupTimeout)
		defer cancel()
		err = errors.Join(lifecycle.RunError(ctx, err), cleanupStatefulSet(cleanupCtx, sets, workload), lifecycle.RunError(ctx, ctx.Err()))
	}()
	// created supplies the UID for deletion preconditions and dependent ownership.
	created, err := sets.Create(ctx, workload, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create StatefulSet %s/%s: %w", workload.Namespace, workload.Name, err)
	}
	workload = created
	// service gives each replica DNS identity and is deleted with its controller.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: workload.Name, Namespace: workload.Namespace,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workload, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  map[string]string{generatorOwnerLabel: workload.Name},
		},
	}
	_, err = core.Services(workload.Namespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create StatefulSet Service %s/%s: %w", service.Namespace, service.Name, err)
	}
	return waitForStatefulSet(ctx, sets, workload, g.config.Duration.Duration)
}

// newStatefulSet isolates the template and assigns a unique controller identity.
// Parallel management lets replicas start independently as resources permit.
func newStatefulSet(cfg statefulSetConfig) *appsv1.StatefulSet {
	// name separates concurrent runs, even when their user labels match.
	name := envconf.RandomName("scale", 32)
	template := preparePodTemplate(cfg.PodTemplate, name)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: template.Namespace,
			Labels: map[string]string{generatorOwnerLabel: name},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &cfg.Replicas, ServiceName: name,
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: map[string]string{generatorOwnerLabel: name}},
			Template:            template,
		},
	}
}

// waitForStatefulSet starts duration at readiness and permits controller recovery.
// The caller's context bounds startup, duration and any remaining recovery time.
func waitForStatefulSet(ctx context.Context, sets appsclient.StatefulSetInterface, owned *appsv1.StatefulSet, duration time.Duration) error {
	// readyAt records first readiness without resetting on Pod replacement.
	var readyAt time.Time
	readFailures := 0
	// err retains lookup failures and the caller's cancellation cause.
	err := wait.PollUntilContextCancel(ctx, statefulSetPollInterval, true, func(ctx context.Context) (bool, error) {
		// current must still be the controller created by this run.
		current, err := sets.Get(ctx, owned.Name, metav1.GetOptions{})
		if err != nil {
			readFailures++
			// Retry server overload and ordinary connection closure on this read.
			// API status takes precedence, keeping authentication failures terminal.
			transient := apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) ||
				apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err)
			var status apierrors.APIStatus
			if !errors.As(err, &status) {
				transient = netutil.IsProbableEOF(err)
			}
			if ctx.Err() == nil && transient && readFailures < statefulSetReadAttempts {
				slog.WarnContext(ctx, "StatefulSet read delayed", "name", owned.Name,
					"attempt", readFailures, "error", err)
				return false, nil
			}
			return false, err
		}
		readFailures = 0
		if current.UID != owned.UID || current.DeletionTimestamp != nil {
			return false, errors.New("StatefulSet was replaced or is being deleted")
		}
		if current.Status.ObservedGeneration < current.Generation || current.Status.ReadyReplicas != *owned.Spec.Replicas {
			return false, nil
		}
		if readyAt.IsZero() {
			readyAt = time.Now()
		}
		return time.Since(readyAt) >= duration, nil
	})
	if err != nil {
		return fmt.Errorf("wait for StatefulSet %s/%s: %w", owned.Namespace, owned.Name, err)
	}
	return nil
}

// cleanupStatefulSet verifies ownership and waits for foreground deletion.
// A lost Create response is recoverable through the generated ownership label.
func cleanupStatefulSet(ctx context.Context, sets appsclient.StatefulSetInterface, owned *appsv1.StatefulSet) error {
	// current supplies an API UID even when the original Create response was lost.
	current, err := sets.Get(ctx, owned.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find StatefulSet %s/%s for cleanup: %w", owned.Namespace, owned.Name, err)
	}
	if current.Labels[generatorOwnerLabel] != owned.Name || (owned.UID != "" && current.UID != owned.UID) {
		return fmt.Errorf("refuse to delete unowned StatefulSet %s/%s", owned.Namespace, owned.Name)
	}
	// propagation keeps the controller present until its Pods and Service are gone.
	propagation := metav1.DeletePropagationForeground
	err = sets.Delete(ctx, current.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &current.UID}, PropagationPolicy: &propagation,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete StatefulSet %s/%s: %w", owned.Namespace, owned.Name, err)
	}
	err = wait.PollUntilContextCancel(ctx, statefulSetPollInterval, true, func(ctx context.Context) (bool, error) {
		// remaining can be absent or replaced once the owned controller is removed.
		remaining, err := sets.Get(ctx, owned.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return remaining.UID != current.UID, nil
	})
	if err != nil {
		return fmt.Errorf("wait for StatefulSet %s/%s cleanup: %w", owned.Namespace, owned.Name, err)
	}
	return nil
}
