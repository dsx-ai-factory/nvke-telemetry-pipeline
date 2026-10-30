// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/internal/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const jobTestSpec = `context: scale
podTemplate:
  metadata:
    namespace: load
    labels:
      scale.nktp.io/workload: avalanche
  spec:
    containers:
    - name: load
      image: example:test
      command: [/binary]
      args: [metrics, '--rate', '10']
`

func TestNewJobGenerator(t *testing.T) {
	for _, test := range []struct {
		name        string
		spec        string
		parallelism int32
		count       int
		completions int32
		wantError   bool
	}{
		{name: "defaults", spec: "{}", parallelism: 1, count: 1, completions: 1},
		{name: "native template", spec: jobTestSpec, parallelism: 1, count: 1, completions: 1},
		{name: "parallelism", spec: jobTestSpec + "parallelism: 3", parallelism: 3, count: 1, completions: 3},
		{name: "multiple jobs", spec: "count: 3", parallelism: 1, count: 3, completions: 1},
		{name: "zero count", spec: "count: 0", parallelism: 1, completions: 1},
		{name: "negative count", spec: "count: -1", wantError: true},
		{name: "fractional count", spec: "count: 1.5", wantError: true},
		{name: "independent completions", spec: "parallelism: 3\ncompletions: 12", parallelism: 3, count: 1, completions: 12},
		{name: "zero completions", spec: "completions: 0", wantError: true},
		{name: "negative completions", spec: "completions: -1", wantError: true},
		{name: "zero parallelism", spec: "parallelism: 0", wantError: true},
		{name: "negative parallelism", spec: "parallelism: -1", wantError: true},
		{name: "unknown generator field", spec: "replicas: 2", wantError: true},
		{name: "controller label", spec: "podTemplate: {metadata: {labels: {batch.kubernetes.io/job-name: foreign}}}", wantError: true},
		{name: "legacy controller label", spec: "podTemplate: {metadata: {labels: {controller-uid: foreign}}}", wantError: true},
		{name: "ownership label", spec: "podTemplate: {metadata: {labels: {scale.nktp.io/generator: foreign}}}", wantError: true},
		{name: "API validates fields", spec: "podTemplate: {spec: {restartPolicy: invalid}}", parallelism: 1, count: 1, completions: 1},
		{name: "nonmapping", spec: "[]", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, err := NewJobGenerator(parseSpec(t, test.spec), "/does/not/exist")
			if test.wantError {
				assert.Error(t, err, "NewJobGenerator()")
				return
			}
			require.NoError(t, err, "NewJobGenerator()")
			assert.Equal(t, test.parallelism, g.config.Parallelism, "parallelism")
			assert.Equal(t, test.count, g.config.Count, "count")
			assert.Equal(t, test.completions, *g.config.Completions, "completions")
		})
	}
}

func TestJobTopologySpread(t *testing.T) {
	g, err := NewJobGenerator(parseSpec(t, jobTestSpec+"parallelism: 3\n"), "/does/not/exist")
	require.NoError(t, err, "NewJobGenerator()")
	first := g.manifest(time.Now().Add(time.Minute))
	second := g.manifest(time.Now().Add(time.Minute))
	require.NotEqual(t, first.Name, second.Name, "independent Job names")
	honor := corev1.NodeInclusionPolicyHonor
	for _, job := range []*batchv1.Job{first, second} {
		want := []corev1.TopologySpreadConstraint{{
			MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.DoNotSchedule,
			NodeAffinityPolicy: &honor, NodeTaintsPolicy: &honor,
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{generatorOwnerLabel: job.Name}},
		}}
		require.Equal(t, want, job.Spec.Template.Spec.TopologySpreadConstraints, "topology spread")
		assert.Nil(t, job.Spec.Selector, "Job controller assigns its selector")
		assert.Equal(t, "load", job.Namespace)
		assert.Equal(t, job.Name, job.Labels[generatorOwnerLabel])
		assert.Equal(t, int32(3), *job.Spec.Parallelism)
		assert.Equal(t, job.Spec.Parallelism, job.Spec.Completions)
		assert.Zero(t, *job.Spec.BackoffLimit)
		assert.Positive(t, *job.Spec.ActiveDeadlineSeconds)
		assert.Positive(t, *job.Spec.TTLSecondsAfterFinished)
		assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
		assert.Equal(t, new(bool), job.Spec.Template.Spec.AutomountServiceAccountToken)
		selector, err := metav1.LabelSelectorAsSelector(want[0].LabelSelector)
		require.NoError(t, err, "parse selector")
		other := first
		if job == first {
			other = second
		}
		assert.True(t, selector.Matches(labels.Set(job.Spec.Template.Labels)), "count this Job's Pods")
		assert.False(t, selector.Matches(labels.Set(other.Spec.Template.Labels)), "ignore another Job's Pods")
	}
}

func TestJobManifestPreservesTemplate(t *testing.T) {
	g, err := NewJobGenerator(parseSpec(t, jobTestSpec+`    restartPolicy: OnFailure
    automountServiceAccountToken: true
    topologySpreadConstraints: []
    nodeSelector: {pool: load}
    volumes:
    - name: scratch
      emptyDir: {}
`), "/does/not/exist")
	require.NoError(t, err, "NewJobGenerator()")
	source := g.config.PodTemplate.DeepCopy()
	job := g.manifest(time.Now().Add(time.Minute))
	assert.Equal(t, corev1.RestartPolicyOnFailure, job.Spec.Template.Spec.RestartPolicy)
	want := source.DeepCopy()
	want.Labels[generatorOwnerLabel] = job.Name
	assert.Equal(t, *want, job.Spec.Template, "native Pod template preserved")
	job.Spec.Template.Labels["scale.nktp.io/workload"] = "changed"
	job.Spec.Template.Spec.Containers[0].Image = "changed"
	assert.Equal(t, *source, g.config.PodTemplate, "manifest does not mutate configuration")
}

// TestJobGeneratorRun checks completion, cancellation and preservation of cleanup errors.
func TestJobGeneratorRun(t *testing.T) {
	createErr := errors.New("create rejected")
	readErr := errors.New("read failed")
	cleanupErr := errors.New("delete rejected")
	for _, test := range []struct {
		name         string
		condition    batchv1.JobConditionType
		createErr    error
		lostResponse bool
		readErr      error
		cancel       bool
		stop         bool
		replaced     bool
		foreignOwner bool
		cleanupErr   error
		wantErr      string
		wantDelete   bool
	}{
		{name: "partial then complete", condition: batchv1.JobComplete, wantDelete: true},
		{name: "failed", condition: batchv1.JobFailed, wantErr: "export failed", wantDelete: true},
		{name: "replaced", replaced: true, wantErr: "job was replaced"},
		{name: "foreign owner", condition: batchv1.JobComplete, foreignOwner: true, wantErr: "refuse to delete unowned"},
		{name: "get failed", readErr: readErr, wantErr: "read failed", wantDelete: true},
		{name: "target reached", stop: true, wantDelete: true},
		{name: "target reached with cleanup failure", stop: true, cleanupErr: cleanupErr, wantErr: "delete Job", wantDelete: true},
		{name: "canceled", cancel: true, wantErr: "context canceled", wantDelete: true},
		{name: "deadline", wantErr: "context deadline exceeded", wantDelete: true},
		{name: "create rejected", createErr: createErr, wantErr: "create Job"},
		{name: "lost create response", createErr: createErr, lostResponse: true, wantErr: "create Job", wantDelete: true},
		{name: "cleanup failed", condition: batchv1.JobComplete, cleanupErr: cleanupErr, wantErr: "delete Job", wantDelete: true},
		{name: "failed and cleanup failed", condition: batchv1.JobFailed, cleanupErr: cleanupErr, wantErr: "export failed", wantDelete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, err := NewJobGenerator(parseSpec(t, jobTestSpec+"parallelism: 3\n"), "/does/not/exist")
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				ctx, stop := context.WithCancelCause(ctx)
				defer stop(nil)
				client := fake.NewClientset()
				resource := batchv1.SchemeGroupVersion.WithResource("jobs")
				var job *batchv1.Job
				client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					job = action.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
					job.UID = "owned"
					if test.createErr != nil && !test.lostResponse {
						return true, nil, test.createErr
					}
					require.NoError(t, client.Tracker().Create(resource, job, job.Namespace))
					if test.lostResponse {
						return true, nil, test.createErr
					}
					if test.cancel {
						cancel()
					}
					if test.stop {
						stop(lifecycle.ErrStageComplete)
					}
					return true, job, nil
				})
				reads := 0
				client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					reads++
					if test.readErr != nil && reads == 1 {
						return true, nil, test.readErr
					}
					obj, err := client.Tracker().Get(resource, action.GetNamespace(), action.(ktesting.GetAction).GetName())
					if err != nil {
						return true, nil, err
					}
					current := obj.(*batchv1.Job)
					if test.replaced {
						current.UID = "replacement"
					}
					if test.foreignOwner {
						current.Labels[generatorOwnerLabel] = "other"
					}
					if test.condition != "" && reads > 1 {
						current.Status.Conditions = []batchv1.JobCondition{{Type: test.condition, Status: corev1.ConditionTrue, Reason: "WorkloadExited", Message: "export failed"}}
					}
					current.Status.Succeeded = 1
					return true, current, nil
				})
				deleted := false
				client.PrependReactor("delete", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					deleted = true
					opts := action.(ktesting.DeleteAction).GetDeleteOptions()
					assert.Equal(t, &metav1.Preconditions{UID: &job.UID}, opts.Preconditions)
					require.NotNil(t, opts.PropagationPolicy)
					assert.Equal(t, metav1.DeletePropagationForeground, *opts.PropagationPolicy)
					if test.cleanupErr != nil {
						return true, nil, test.cleanupErr
					}
					return false, nil, nil
				})
				podReads := 0
				client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
					podReads++
					assert.Equal(t, "batch.kubernetes.io/controller-uid=owned", action.(ktesting.ListAction).GetListRestrictions().Labels.String())
					pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"batch.kubernetes.io/controller-uid": "owned"}}}
					if podReads == 1 {
						pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))}
					}
					return true, &corev1.PodList{Items: []corev1.Pod{pod}}, nil
				})

				start := time.Now()
				err = g.run(ctx, client.BatchV1(), client.CoreV1())
				if test.wantErr == "" {
					assert.NoError(t, err)
					if test.stop {
						assert.Equal(t, time.Second, time.Since(start), "wait for cleanup after successful stopping")
					} else {
						assert.Equal(t, 2*time.Second, time.Since(start), "wait for Job completion and owned Pods to disappear")
					}
				} else {
					require.ErrorContains(t, err, test.wantErr)
				}
				assert.Equal(t, test.wantDelete, deleted)
				if test.cleanupErr != nil {
					assert.ErrorIs(t, err, test.cleanupErr, "cleanup error returned")
				} else if deleted {
					assert.Equal(t, 2, podReads, "wait for owned Pods and ignore unrelated Pods")
					_, getErr := client.Tracker().Get(resource, job.Namespace, job.Name)
					assert.True(t, apierrors.IsNotFound(getErr), "Job removed before returning")
				}
				if test.stop {
					assert.NotErrorIs(t, err, context.Canceled)
				} else if ctx.Err() != nil {
					assert.ErrorIs(t, err, ctx.Err())
					assert.NotContains(t, err.Error(), "cleanup", "cleanup survives run cancellation")
				}
				for _, action := range client.Actions() {
					assert.Equal(t, "load", action.GetNamespace())
				}
			})
		})
	}
}

func TestJobRunRequiresDeadline(t *testing.T) {
	g, err := NewJobGenerator(parseSpec(t, jobTestSpec), "/does/not/exist")
	require.NoError(t, err)
	assert.ErrorContains(t, g.Run(t.Context()), "deadline")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, g.Run(ctx), context.Canceled)
	g.config.Count = 0
	assert.NoError(t, g.Run(t.Context()), "zero count needs neither credentials nor a deadline")
	assert.ErrorIs(t, g.Run(ctx), context.Canceled)
}

func TestJobCleanupWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "uid", Labels: map[string]string{generatorOwnerLabel: "owned"}}}
		client := fake.NewClientset(job)
		client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), jobCleanupTimeout)
		defer cancel()
		err := cleanupJob(ctx, client.BatchV1().Jobs(job.Namespace), client.CoreV1().Pods(job.Namespace), job)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestNewJobGeneratorRequiresKubeconfig(t *testing.T) {
	for _, path := range []string{"", "relative/kubeconfig"} {
		t.Run(path, func(t *testing.T) {
			_, err := NewJobGenerator(parseSpec(t, jobTestSpec), path)
			assert.Error(t, err, "NewJobGenerator() requires an absolute kubeconfig path")
		})
	}
}

func TestJobGeneratorCount(t *testing.T) {
	for _, failCreate := range []bool{false, true} {
		name := "complete"
		if failCreate {
			name = "partial create"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, err := NewJobGenerator(parseSpec(t, jobTestSpec+"count: 3\nparallelism: 2\ncompletions: 8"), "/does/not/exist")
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Namespace: "load"}}
				client := fake.NewClientset(foreign)
				resource := batchv1.SchemeGroupVersion.WithResource("jobs")
				createErr := errors.New("create rejected")
				var created []*batchv1.Job
				attempts := 0
				client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					attempts++
					if failCreate && attempts == 2 {
						return true, nil, createErr
					}
					job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
					job.UID = types.UID(job.Name)
					created = append(created, job)
					return true, job, client.Tracker().Create(resource, job, job.Namespace)
				})
				client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
					obj, err := client.Tracker().Get(resource, action.GetNamespace(), action.(ktesting.GetAction).GetName())
					if err != nil {
						return true, nil, err
					}
					job := obj.(*batchv1.Job)
					if !failCreate && len(created) == g.config.Count {
						job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
					}
					return true, job, nil
				})

				err = g.run(ctx, client.BatchV1(), client.CoreV1())
				if failCreate {
					assert.ErrorIs(t, err, createErr)
					assert.NotEmpty(t, created, "a sibling started before the failure")
				} else {
					assert.NoError(t, err)
					assert.Len(t, created, 3, "all Jobs started before any completed")
				}
				names := make(map[string]bool)
				for _, job := range created {
					assert.False(t, names[job.Name], "Job names must be unique")
					names[job.Name] = true
					assert.Equal(t, job.Name, job.Labels[generatorOwnerLabel])
					assert.Equal(t, job.Name, job.Spec.Template.Labels[generatorOwnerLabel])
					assert.Equal(t, int32(2), *job.Spec.Parallelism)
					assert.Equal(t, int32(8), *job.Spec.Completions)
				}
				remaining, err := client.BatchV1().Jobs("load").List(t.Context(), metav1.ListOptions{})
				require.NoError(t, err)
				assert.Equal(t, []batchv1.Job{*foreign}, remaining.Items, "clean up every owned Job, including after partial startup")
			})
		})
	}
}
