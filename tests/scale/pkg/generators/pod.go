// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	netutil "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	// podPollInterval spaces completion and cleanup checks.
	podPollInterval = time.Second
	// podReadAttempts bounds consecutive transient failures of one Pod read.
	podReadAttempts = 3
	// podCleanupTimeout bounds cleanup independently of run cancellation.
	podCleanupTimeout = 5 * time.Minute
)

// PodGenerator runs a finite batch of Pods. Construct it with NewPodGenerator.
type PodGenerator struct {
	// kubeconfig names the credential file shared by the plan.
	kubeconfig string
	// config holds the native template and its repetition count.
	config podConfig
}

// podConfig repeats one native Pod template without controller semantics.
type podConfig struct {
	// Count is the number of Pods created once, without replacement.
	Count int `json:"count"`
	// PodTemplate supplies Pod metadata, containers and scheduling settings.
	PodTemplate corev1.PodTemplateSpec `json:"podTemplate"`
}

// NewPodGenerator parses a native Pod template without reading credentials or
// contacting Kubernetes. Count defaults to one. Run defaults the namespace to default.
// A zero count creates no Pods. Kubernetes validates the submitted Pod fields.
func NewPodGenerator(spec yaml.Node, kubeconfig string) (*PodGenerator, error) {
	cfg := podConfig{Count: 1}
	if err := decodeSpec(spec, &cfg); err != nil {
		return nil, fmt.Errorf("decode pod spec: %w", err)
	}
	if cfg.Count < 0 {
		return nil, errors.New("count must not be negative")
	}
	return &PodGenerator{kubeconfig: kubeconfig, config: cfg}, nil
}

// Run creates the batch immediately and waits for every Pod to succeed. Failure,
// disappearance or replacement of a Pod fails the run. Pending Pods wait for
// scheduling without checking Nodes. Owned Pods are removed before returning,
// with up to five minutes for cleanup after cancellation.
func (g *PodGenerator) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return lifecycle.RunError(ctx, err)
	}
	if g.config.Count == 0 {
		return nil
	}
	// kubeconfig selects the supplied file's current context.
	kubeconfig, err := clientcmd.LoadFromFile(g.kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	// config bounds individual API calls as well as the overall run context.
	config, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, "", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	config.Timeout = 30 * time.Second
	// Let the scale workload reach the API without client-side throttling.
	config.QPS, config.Burst = -1, -1
	// core provides the typed Pod API without a separate client abstraction.
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Pod client: %w", err)
	}
	return g.run(ctx, core)
}

// run requires the cancellation and count checks performed by Run.
func (g *PodGenerator) run(ctx context.Context, core coreclient.CoreV1Interface) (err error) {
	// owner separates concurrent invocations, including those sharing run_id.
	owner := envconf.RandomName("scale", 32)
	template := preparePodTemplate(g.config.PodTemplate, owner)
	pods := core.Pods(template.Namespace)
	// owned records attempted creates so cleanup can recover a lost response.
	var owned []*corev1.Pod
	defer func() {
		// cleanupCtx remains usable after the plan is canceled.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), podCleanupTimeout)
		defer cancel()
		err = errors.Join(lifecycle.RunError(ctx, err), cleanupPods(cleanupCtx, pods, owner, owned), lifecycle.RunError(ctx, ctx.Err()))
	}()
	for i := 0; i < g.config.Count; i++ {
		// Each replica has independent metadata and native Pod fields.
		replica := template.DeepCopy()
		// pod preserves native settings while assigning invocation-owned identity.
		pod := &corev1.Pod{ObjectMeta: replica.ObjectMeta, Spec: replica.Spec}
		pod.Name = fmt.Sprintf("%s-%d", owner, i)
		pod.GenerateName = ""
		owned = append(owned, pod)
		// created includes the API-assigned UID needed to detect replacement.
		created, err := pods.Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create Pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		owned[len(owned)-1] = created
	}
	return waitForPods(ctx, pods, owner, owned)
}

// waitForPods observes the whole batch so Pods can complete at different times.
func waitForPods(ctx context.Context, pods coreclient.PodInterface, owner string, owned []*corev1.Pod) error {
	// err retains context and API failures from polling for the caller.
	err := wait.PollUntilContextCancel(ctx, podPollInterval, true, func(ctx context.Context) (bool, error) {
		// list contains only Pods labeled for this invocation.
		var list *corev1.PodList
		// err includes the last read failure if the retry budget is exhausted.
		err := retryPodRead(ctx, func(ctx context.Context) error {
			// readErr belongs to this attempt, independently of the polling result.
			var readErr error
			list, readErr = pods.List(ctx, metav1.ListOptions{LabelSelector: generatorOwnerLabel + "=" + owner})
			return readErr
		})
		if err != nil {
			return false, err
		}
		// current indexes the observed batch by its generated names.
		current := make(map[string]*corev1.Pod, len(list.Items))
		for i := range list.Items {
			current[list.Items[i].Name] = &list.Items[i]
		}
		// complete requires success from every expected Pod, not simultaneous Running.
		complete := true
		for _, expected := range owned {
			// pod must still have the identity returned by Create.
			pod := current[expected.Name]
			if pod == nil || pod.UID != expected.UID {
				return false, fmt.Errorf("pod %s/%s disappeared or was replaced", expected.Namespace, expected.Name)
			}
			switch pod.Status.Phase {
			case corev1.PodFailed:
				return false, fmt.Errorf("pod %s/%s failed: %s: %s", pod.Namespace, pod.Name, pod.Status.Reason, pod.Status.Message)
			case corev1.PodSucceeded:
			default:
				complete = false
			}
		}
		return complete, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Pods: %w", err)
	}
	return nil
}

// cleanupPods deletes only this invocation's Pods, recovering unknown UIDs after
// lost Create responses. All deletes precede waiting so grace periods overlap.
// The caller must supply a bounded context independent of run cancellation.
func cleanupPods(ctx context.Context, pods coreclient.PodInterface, owner string, owned []*corev1.Pod) error {
	// errs retains individual failures while cleanup continues for other Pods.
	var errs []error
	// remaining tracks identities whose deletion was successfully requested.
	remaining := make(map[string]types.UID)
	for _, expected := range owned {
		// current verifies ownership before any destructive API call.
		current, err := getPodForCleanup(ctx, pods, expected.Name)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("find Pod %s for cleanup: %w", expected.Name, err))
			continue
		}
		if current.Labels[generatorOwnerLabel] != owner || (expected.UID != "" && current.UID != expected.UID) {
			errs = append(errs, fmt.Errorf("refuse to delete unowned Pod %s/%s", current.Namespace, current.Name))
			continue
		}
		err = pods.Delete(ctx, current.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &current.UID}})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("delete Pod %s/%s: %w", current.Namespace, current.Name, err))
			continue
		}
		remaining[current.Name] = current.UID
	}
	// err joins failures to confirm deletion with earlier lookup/delete failures.
	err := wait.PollUntilContextCancel(ctx, podPollInterval, true, func(ctx context.Context) (bool, error) {
		for name, uid := range remaining {
			// current may be absent or replaced after the original Pod is deleted.
			current, err := getPodForCleanup(ctx, pods, name)
			if apierrors.IsNotFound(err) || (err == nil && current.UID != uid) {
				delete(remaining, name)
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("wait for Pod %s cleanup: %w", name, err))
				delete(remaining, name)
			}
		}
		return len(remaining) == 0, nil
	})
	return errors.Join(append(errs, err)...)
}

// getPodForCleanup gives each Pod GET its own retry budget. NotFound is returned
// immediately so cleanup can treat an absent Pod as success.
func getPodForCleanup(ctx context.Context, pods coreclient.PodInterface, name string) (*corev1.Pod, error) {
	// pod holds the result of the last GET attempt.
	var pod *corev1.Pod
	// err reports retry exhaustion or cancellation without discarding the cause.
	err := retryPodRead(ctx, func(ctx context.Context) error {
		// readErr belongs to this GET attempt.
		var readErr error
		pod, readErr = pods.Get(ctx, name, metav1.GetOptions{})
		return readErr
	})
	return pod, err
}

// retryPodRead retries transient read failures at the Pod polling interval.
// The counter belongs to this call, so successful reads reset the budget and
// different Pod names never share it. Client-go's own retries remain in effect.
func retryPodRead(ctx context.Context, read func(context.Context) error) error {
	// failures counts consecutive failed calls, including the initial attempt.
	failures := 0
	return wait.PollUntilContextCancel(ctx, podPollInterval, true, func(ctx context.Context) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// err is returned unchanged when the read cannot be retried.
		err := read(ctx)
		if err == nil {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		failures++
		if failures >= podReadAttempts || !transientPodReadError(err) {
			return false, err
		}
		return false, nil
	})
}

// transientPodReadError identifies API and transport failures worth retrying.
// Invalid requests, authorization failures and missing resources are terminal.
func transientPodReadError(err error) bool {
	if apierrors.IsTooManyRequests(err) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return true
	}
	// status takes precedence over transport heuristics, including error text.
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		// code uses Kubernetes's decoded response instead of parsing error text.
		code := status.Status().Code
		return code >= 500 && code < 600
	}
	// networkErr covers transport timeouts while the caller's context is live.
	var networkErr net.Error
	return (errors.As(err, &networkErr) && networkErr.Timeout()) ||
		netutil.IsConnectionReset(err) || netutil.IsConnectionRefused(err) ||
		netutil.IsHTTP2ConnectionLost(err) || netutil.IsProbableEOF(err)
}
