// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"go.yaml.in/yaml/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	// jobPollInterval spaces Job status and cleanup checks.
	jobPollInterval = time.Second

	// jobCleanupTimeout gives resource deletion its own budget after cancellation.
	jobCleanupTimeout = 30 * time.Second
)

// JobGenerator runs a native Pod template to completion in Kubernetes Jobs.
type JobGenerator struct {
	// kubeconfig names the explicit credential file shared by the plan.
	kubeconfig string
	// config holds the validated workload settings.
	config jobConfig
}

// jobConfig is the plan's Job specification. Kubeconfig selection is supplied
// separately so it can be shared across the plan.
type jobConfig struct {
	Context string `json:"context"`
	Count   int    `json:"count"`
	// Parallelism limits concurrent Pods in each Job.
	Parallelism int32 `json:"parallelism"`
	// Completions defaults to Parallelism when omitted.
	Completions *int32                 `json:"completions"`
	PodTemplate corev1.PodTemplateSpec `json:"podTemplate"`
}

// NewJobGenerator parses a native Pod template without reading credentials or
// contacting Kubernetes. Kubeconfig must be absolute. Context defaults to that
// file's current context. Count defaults to one, and zero creates no Jobs.
// Parallelism and completions must be positive.
// Run defaults the namespace to default. Kubernetes validates native Pod fields.
func NewJobGenerator(spec yaml.Node, kubeconfig string) (*JobGenerator, error) {
	if spec.Kind != yaml.MappingNode {
		return nil, errors.New("job spec must be a mapping")
	}
	cfg := jobConfig{Count: 1, Parallelism: 1}
	if err := decodeSpec(spec, &cfg); err != nil {
		return nil, fmt.Errorf("decode job spec: %w", err)
	}
	if cfg.Count < 0 {
		return nil, errors.New("count must not be negative")
	}
	if cfg.Parallelism < 1 {
		return nil, errors.New("parallelism must be positive")
	}
	if cfg.Completions == nil {
		cfg.Completions = new(cfg.Parallelism)
	}
	if *cfg.Completions < 1 {
		return nil, errors.New("completions must be positive")
	}
	for key := range cfg.PodTemplate.Labels {
		if key == generatorOwnerLabel || key == "job-name" || key == "controller-uid" || strings.HasPrefix(key, "batch.kubernetes.io/") {
			return nil, fmt.Errorf("pod label %q is reserved for Job ownership", key)
		}
	}
	if !filepath.IsAbs(kubeconfig) {
		return nil, errors.New("job requires an absolute kubeconfig path")
	}
	return &JobGenerator{kubeconfig: kubeconfig, config: cfg}, nil
}

// Run submits Jobs and waits for their completions or a failure.
// The context must have a deadline. Run finishes cleanup of owned resources
// before returning, with up to 30 seconds for cleanup after cancellation.
func (g *JobGenerator) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return lifecycle.RunError(ctx, err)
	}
	if g.config.Count == 0 {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("job execution requires a context deadline")
	}
	kubeconfig, err := clientcmd.LoadFromFile(g.kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, g.config.Context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return fmt.Errorf("configure Kubernetes client: %w", err)
	}
	config.Timeout = 10 * time.Second
	// Match the other generators when observing and cleaning up many workloads.
	config.QPS, config.Burst = -1, -1
	batch, err := batchclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Job client: %w", err)
	}
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create Pod client: %w", err)
	}
	return g.run(ctx, batch, core)
}

// run requires the context deadline checked by Run.
func (g *JobGenerator) run(ctx context.Context, batch batchclient.BatchV1Interface, core coreclient.CoreV1Interface) error {
	return runInstances(ctx, g.config.Count, func(ctx context.Context) error {
		return g.runJob(ctx, batch, core)
	})
}

// runJob owns one Job and retains cleanup failures after a requested stop.
func (g *JobGenerator) runJob(ctx context.Context, batch batchclient.BatchV1Interface, core coreclient.CoreV1Interface) (err error) {
	deadline, _ := ctx.Deadline()
	job := g.manifest(deadline)
	jobs, pods := batch.Jobs(job.Namespace), core.Pods(job.Namespace)
	// Install cleanup before Create: a failed response can still mean that the
	// API server accepted the Job. Its unique name and label allow reconciliation.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobCleanupTimeout)
		defer cancel()
		err = errors.Join(lifecycle.RunError(ctx, err), cleanupJob(cleanupCtx, jobs, pods, job), lifecycle.RunError(ctx, ctx.Err()))
	}()
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	job = created
	err = wait.PollUntilContextCancel(ctx, jobPollInterval, true, func(ctx context.Context) (bool, error) {
		current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != job.UID {
			return false, errors.New("job was replaced")
		}
		for _, condition := range current.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobFailed, batchv1.JobFailureTarget:
				return false, fmt.Errorf("%s: %s", condition.Reason, condition.Message)
			case batchv1.JobComplete:
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Job %s/%s: %w", job.Namespace, job.Name, err)
	}
	return nil
}

// manifest builds a uniquely named Job without contacting Kubernetes. Its deadline
// and TTL let the controllers stop and remove the Job if the harness crashes.
func (g *JobGenerator) manifest(deadline time.Time) *batchv1.Job {
	name := envconf.RandomName("scale", 32)
	template := preparePodTemplate(g.config.PodTemplate, name)
	if template.Spec.RestartPolicy == "" {
		template.Spec.RestartPolicy = corev1.RestartPolicyNever
	}
	if template.Spec.AutomountServiceAccountToken == nil {
		noToken := false
		template.Spec.AutomountServiceAccountToken = &noToken
	}
	if template.Spec.TopologySpreadConstraints == nil {
		honor := corev1.NodeInclusionPolicyHonor
		template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.DoNotSchedule,
			NodeAffinityPolicy: &honor, NodeTaintsPolicy: &honor,
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{generatorOwnerLabel: name}},
		}}
	}
	parallelism, completions, zero := g.config.Parallelism, *g.config.Completions, int32(0)
	// Controller bounds also cover a lost Create response or a crashed harness.
	activeSeconds := max(int64(1), int64(time.Until(deadline).Seconds())+1)
	ttlSeconds := int32(60)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: template.Namespace, Labels: map[string]string{generatorOwnerLabel: name}},
		Spec: batchv1.JobSpec{
			Parallelism: &parallelism, Completions: &completions, BackoffLimit: &zero,
			ActiveDeadlineSeconds: &activeSeconds, TTLSecondsAfterFinished: &ttlSeconds,
			Template: template,
		},
	}
}

// cleanupJob checks the ownership label and any known UID before deleting the Job,
// then waits for that Job and its owned Pods to disappear. It can recover an
// unknown UID by name after a lost Create response. The caller must supply a
// bounded context independent of run cancellation.
func cleanupJob(ctx context.Context, jobs batchclient.JobInterface, pods coreclient.PodInterface, job *batchv1.Job) error {
	current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && job.UID == "" {
		return nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("find Job for cleanup: %w", err)
	}
	uid := job.UID
	if err == nil {
		if current.Labels[generatorOwnerLabel] != job.Name || (uid != "" && current.UID != uid) {
			return fmt.Errorf("refuse to delete unowned Job %s/%s", job.Namespace, job.Name)
		}
		uid = current.UID
		policy := metav1.DeletePropagationForeground
		err = jobs.Delete(ctx, job.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: &policy})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete Job %s/%s: %w", job.Namespace, job.Name, err)
		}
	}
	err = wait.PollUntilContextCancel(ctx, jobPollInterval, true, func(ctx context.Context) (bool, error) {
		current, err := jobs.Get(ctx, job.Name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if err == nil && current.UID == uid {
			return false, nil
		}
		remaining, err := pods.List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/controller-uid=" + string(uid)})
		if err != nil {
			return false, err
		}
		for _, pod := range remaining.Items {
			if metav1.IsControlledBy(&pod, &metav1.ObjectMeta{UID: uid}) {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Job %s/%s cleanup: %w", job.Namespace, job.Name, err)
	}
	return nil
}
