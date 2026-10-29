// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	// kubernetesNodePollInterval spaces readiness and deletion checks.
	kubernetesNodePollInterval = time.Second
	// kubernetesNodeCleanupTimeout prevents one stuck deletion from holding a worker.
	kubernetesNodeCleanupTimeout = 30 * time.Second
	// kubernetesNodeCleanupWorkers bounds concurrent node deletions.
	kubernetesNodeCleanupWorkers = 32
	// kubernetesNodeQPS limits requests across all node operations.
	kubernetesNodeQPS = 100
	// kubernetesOwnerLabel identifies nodes created by one generator invocation.
	kubernetesOwnerLabel = "scale.nktp.io/generator"
	// kwokNodeKey selects nodes for KWOK and taints them against ordinary workloads.
	kwokNodeKey = "kwok.x-k8s.io/node"
)

// KubernetesGenerator creates temporary groups of KWOK nodes.
// KWOK must already be installed in the cluster.
type KubernetesGenerator struct {
	// kubeconfig is the explicit credential file supplied by the plan.
	kubeconfig string
	// config holds parsed templates and run settings with defaults applied.
	config kubernetesConfig
}

// kubernetesConfig describes the groups and duration of one generator run.
type kubernetesConfig struct {
	// Duration starts after every generated node becomes Ready.
	Duration metav1.Duration `json:"duration"`
	// NodeGroups supplies the templates and replica counts to create.
	NodeGroups []kubernetesNodeGroup `json:"nodeGroups"`
}

// kubernetesNodeGroup repeats a native Node template under a unique group name.
type kubernetesNodeGroup struct {
	// Name distinguishes the group and prefixes its generated node names.
	Name string `json:"name"`
	// Replicas defaults to 1000 when nil. A pointer distinguishes omission from zero.
	Replicas *int `json:"replicas"`
	// NodeTemplate is copied for each node and validated by the Kubernetes API.
	NodeTemplate corev1.Node `json:"nodeTemplate"`
}

// NewKubernetesGenerator parses native Node templates and checks run settings.
// Each group defaults to 1000 replicas. Kubernetes validates the nodes on creation.
// The supplied kubeconfig determines the cluster and is only read during Run.
func NewKubernetesGenerator(spec yaml.Node, kubeconfig string) (*KubernetesGenerator, error) {
	var cfg kubernetesConfig
	if err := decodeSpec(spec, &cfg); err != nil {
		return nil, fmt.Errorf("decode kubernetes spec: %w", err)
	}
	if cfg.Duration.Duration <= 0 {
		return nil, errors.New("duration must be positive")
	}
	if len(cfg.NodeGroups) == 0 {
		return nil, errors.New("nodeGroups must contain at least one group")
	}
	names := make(map[string]bool)
	for i := range cfg.NodeGroups {
		group := &cfg.NodeGroups[i]
		if group.Name == "" || names[group.Name] {
			return nil, fmt.Errorf("node group name %q must be nonempty and unique", group.Name)
		}
		names[group.Name] = true
		if group.Replicas == nil {
			replicas := 1000
			group.Replicas = &replicas
		}
		if *group.Replicas < 1 {
			return nil, fmt.Errorf("node group %q: replicas must be positive", group.Name)
		}
	}
	return &KubernetesGenerator{kubeconfig: kubeconfig, config: cfg}, nil
}

// Run creates the nodes, waits for readiness, keeps them for duration and deletes
// them. Cleanup runs even after cancellation with a timeout sized for the nodes.
// The returned error includes both run and cleanup failures.
func (g *KubernetesGenerator) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return lifecycle.RunError(ctx, err)
	}
	credentials, err := clientcmd.LoadFromFile(g.kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*credentials, "", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	config.Timeout = 10 * time.Second
	// Permit the default 1000-node run without client-side throttling at 5 QPS.
	config.QPS, config.Burst = kubernetesNodeQPS, kubernetesNodeQPS
	client, err := coreclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Node client: %w", err)
	}
	return g.run(ctx, client.Nodes())
}

// run requires the cancellation check performed by Run.
func (g *KubernetesGenerator) run(ctx context.Context, nodes coreclient.NodeInterface) (err error) {
	// owner isolates repeated runs that use the same templates and labels.
	owner := envconf.RandomName("scale", 32)
	// owned includes attempted creates so cleanup can recover lost responses.
	var owned []*corev1.Node
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), kubernetesNodeCleanupBudget(len(owned)))
		defer cancel()
		err = errors.Join(lifecycle.RunError(ctx, err), cleanupKubernetesNodes(cleanupCtx, nodes, owned), lifecycle.RunError(ctx, ctx.Err()))
	}()
	for _, group := range g.config.NodeGroups {
		for i := range *group.Replicas {
			node := kubernetesNode(group, owner, i)
			// Record the name before Create so cleanup can recover a lost response.
			owned = append(owned, node)
			created, err := nodes.Create(ctx, node, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("create Node %s: %w", node.Name, err)
			}
			owned[len(owned)-1] = created
		}
	}
	if err := waitForKubernetesNodes(ctx, nodes, owner, len(owned)); err != nil {
		return fmt.Errorf("wait for emulated Nodes: %w", err)
	}
	timer := time.NewTimer(g.config.Duration.Duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// kubernetesNode copies the group template and adds a unique name, ownership,
// and KWOK metadata. Omitted allocatable resources default to capacity.
func kubernetesNode(group kubernetesNodeGroup, owner string, index int) *corev1.Node {
	node := group.NodeTemplate.DeepCopy()
	node.Name = fmt.Sprintf("%s-%s-%04d", group.Name, owner, index)
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[kubernetesOwnerLabel] = owner
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[kwokNodeKey] = "fake"
	node.Spec.Taints = slices.DeleteFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
		return taint.Key == kwokNodeKey
	})
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: kwokNodeKey, Value: "fake", Effect: corev1.TaintEffectNoSchedule})
	if node.Status.Allocatable == nil {
		node.Status.Allocatable = node.Status.Capacity.DeepCopy()
	}
	return node
}

// waitForKubernetesNodes waits until every node selected by owner is Ready.
// A missing node or API error fails the wait. The caller controls its deadline.
func waitForKubernetesNodes(ctx context.Context, nodes coreclient.NodeInterface, owner string, count int) error {
	return wait.PollUntilContextCancel(ctx, kubernetesNodePollInterval, true, func(ctx context.Context) (bool, error) {
		current, err := nodes.List(ctx, metav1.ListOptions{LabelSelector: kubernetesOwnerLabel + "=" + owner})
		if err != nil {
			return false, err
		}
		if len(current.Items) != count {
			return false, fmt.Errorf("found %d owned Nodes, want %d", len(current.Items), count)
		}
		for _, node := range current.Items {
			if !slices.ContainsFunc(node.Status.Conditions, func(condition corev1.NodeCondition) bool {
				return condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
			}) {
				return false, nil
			}
		}
		return true, nil
	})
}

// kubernetesNodeCleanupBudget allows every worker batch its full per-node timeout.
// The additional allowance covers at least Get, Delete and confirmation Get per
// node at the client's QPS limit. Small runs retain a five-minute minimum.
func kubernetesNodeCleanupBudget(count int) time.Duration {
	batches := (count + kubernetesNodeCleanupWorkers - 1) / kubernetesNodeCleanupWorkers
	requests := time.Duration(count) * 3 * time.Second / kubernetesNodeQPS
	return max(5*time.Minute, time.Duration(batches)*kubernetesNodeCleanupTimeout+requests)
}

// cleanupKubernetesNodes distributes nodes across a fixed number of workers.
// A worker continues after each error or node timeout. Each owns disjoint error
// slots, which are joined only after every worker has stopped.
func cleanupKubernetesNodes(ctx context.Context, nodes coreclient.NodeInterface, owned []*corev1.Node) error {
	errs := make([]error, len(owned))
	var workers sync.WaitGroup
	for worker := range min(len(owned), kubernetesNodeCleanupWorkers) {
		workers.Go(func() {
			for i := worker; i < len(owned); i += kubernetesNodeCleanupWorkers {
				if ctx.Err() != nil {
					return
				}
				nodeCtx, cancel := context.WithTimeout(ctx, kubernetesNodeCleanupTimeout)
				errs[i] = cleanupKubernetesNode(nodeCtx, nodes, owned[i])
				cancel()
			}
		})
	}
	workers.Wait()
	return errors.Join(append(errs, ctx.Err())...)
}

// cleanupKubernetesNode checks ownership even when a Create response was lost.
// The UID precondition prevents deleting a replacement node.
func cleanupKubernetesNode(ctx context.Context, nodes coreclient.NodeInterface, original *corev1.Node) error {
	current, err := nodes.Get(ctx, original.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find Node %s for cleanup: %w", original.Name, err)
	}
	if current.Labels[kubernetesOwnerLabel] != original.Labels[kubernetesOwnerLabel] || (original.UID != "" && original.UID != current.UID) {
		return fmt.Errorf("refuse to delete unowned Node %s", original.Name)
	}
	uid := current.UID
	if err := nodes.Delete(ctx, original.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Node %s: %w", original.Name, err)
	}
	err = wait.PollUntilContextCancel(ctx, kubernetesNodePollInterval, true, func(ctx context.Context) (bool, error) {
		node, err := nodes.Get(ctx, original.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return node.UID != uid, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Node %s cleanup: %w", original.Name, err)
	}
	return nil
}
