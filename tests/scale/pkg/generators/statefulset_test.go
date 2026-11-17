// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// statefulSetTestSpec includes native fields that must survive construction.
const statefulSetTestSpec = `replicas: 3
duration: 1s
podTemplate:
  metadata:
    namespace: workloads
    labels:
      run_id: example
    annotations:
      example.com/note: preserved
  spec:
    terminationGracePeriodSeconds: 7
    nodeSelector:
      pool: load
    containers:
    - name: load
      image: example:test
      command: [/load]
      args: ["--rate", "10"]
      resources:
        requests:
          cpu: 100m
          memory: 128Mi
`

func TestNewStatefulSetGenerator(t *testing.T) {
	for _, test := range []struct {
		name         string
		spec         string
		wantReplicas int32
		wantCount    int
		wantErr      bool
	}{
		{name: "defaults", spec: "duration: 1s", wantReplicas: 1, wantCount: 1},
		{name: "zero", spec: "duration: 1s\nreplicas: 0", wantCount: 1},
		{name: "no scale ceiling", spec: "duration: 1s\nreplicas: 100000", wantReplicas: 100000, wantCount: 1},
		{name: "Kubernetes validates Pod fields", spec: "duration: 1s\npodTemplate: {spec: {restartPolicy: invalid}}", wantReplicas: 1, wantCount: 1},
		{name: "multiple sets", spec: "duration: 1s\ncount: 2\nreplicas: 3", wantCount: 2, wantReplicas: 3},
		{name: "zero count", spec: "duration: 1s\ncount: 0", wantReplicas: 1},
		{name: "negative count", spec: "duration: 1s\ncount: -1", wantErr: true},
		{name: "fractional count", spec: "duration: 1s\ncount: 1.5", wantErr: true},
		{name: "negative replicas", spec: "duration: 1s\nreplicas: -1", wantErr: true},
		{name: "missing duration", spec: "{}", wantErr: true},
		{name: "zero duration", spec: "duration: 0s", wantErr: true},
		{name: "negative duration", spec: "duration: -1s", wantErr: true},
		{name: "unknown field", spec: "duration: 1s\nimage: invalid", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, err := NewStatefulSetGenerator(parseSpec(t, test.spec), "/does/not/exist")
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantReplicas, g.config.Replicas)
			assert.Equal(t, test.wantCount, g.config.Count)
		})
	}
}

func TestStatefulSetRunWithoutCredentials(t *testing.T) {
	g, err := NewStatefulSetGenerator(parseSpec(t, "duration: 1s\nreplicas: 0"), "/does/not/exist")
	require.NoError(t, err)
	assert.NoError(t, g.Run(t.Context()))
	g.config.Replicas = 1
	g.config.Count = 0
	assert.NoError(t, g.Run(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, g.Run(ctx), context.Canceled)
}

func TestStatefulSetManifest(t *testing.T) {
	g, err := NewStatefulSetGenerator(parseSpec(t, statefulSetTestSpec), "/does/not/exist")
	require.NoError(t, err)
	source := g.config.PodTemplate.DeepCopy()
	workload := newStatefulSet(g.config)
	assert.Equal(t, new(int64(7)), workload.Spec.Template.Spec.TerminationGracePeriodSeconds)
	want := source.DeepCopy()
	want.Labels[generatorOwnerLabel] = workload.Name
	assert.Equal(t, *want, workload.Spec.Template)
	assert.Equal(t, "workloads", workload.Namespace)
	assert.Equal(t, workload.Name, workload.Spec.ServiceName)
	assert.Equal(t, workload.Name, workload.Labels[generatorOwnerLabel])
	assert.Equal(t, &metav1.LabelSelector{MatchLabels: map[string]string{generatorOwnerLabel: workload.Name}}, workload.Spec.Selector)
	assert.Equal(t, &g.config.Replicas, workload.Spec.Replicas)
	assert.Equal(t, appsv1.ParallelPodManagement, workload.Spec.PodManagementPolicy)
	workload.Spec.Template.Labels[generatorOwnerLabel] = "changed"
	workload.Spec.Template.Spec.Containers[0].Image = "changed"
	assert.Equal(t, *source, g.config.PodTemplate)
}

func TestStatefulSetGeneratorRun(t *testing.T) {
	createErr := errors.New("create rejected")
	serviceErr := errors.New("service rejected")
	cleanupErr := errors.New("delete rejected")
	for _, test := range []struct {
		name         string
		createErr    error
		lostResponse bool
		serviceErr   error
		cleanupErr   error
		replaced     bool
		cancelAt     int
		wantErr      string
		wantDelete   bool
	}{
		{name: "ready with recovery", wantDelete: true},
		{name: "replaced", replaced: true, wantErr: "StatefulSet was replaced"},
		{name: "cancel startup", cancelAt: 1, wantErr: "context canceled", wantDelete: true},
		{name: "cancel duration", cancelAt: 4, wantErr: "context canceled", wantDelete: true},
		{name: "create rejected", createErr: createErr, wantErr: "create StatefulSet"},
		{name: "lost create response", createErr: createErr, lostResponse: true, wantErr: "create StatefulSet", wantDelete: true},
		{name: "service rejected", serviceErr: serviceErr, wantErr: "create StatefulSet Service", wantDelete: true},
		{name: "cleanup rejected", cleanupErr: cleanupErr, wantErr: "delete StatefulSet", wantDelete: true},
		{name: "service and cleanup rejected", serviceErr: serviceErr, cleanupErr: cleanupErr, wantErr: "create StatefulSet Service", wantDelete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, err := NewStatefulSetGenerator(parseSpec(t, statefulSetTestSpec), "/does/not/exist")
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				client := fake.NewClientset()
				resource := appsv1.SchemeGroupVersion.WithResource("statefulsets")
				var workload *appsv1.StatefulSet
				client.PrependReactor("create", "statefulsets", func(action ktesting.Action) (bool, runtime.Object, error) {
					workload = action.(ktesting.CreateAction).GetObject().(*appsv1.StatefulSet).DeepCopy()
					workload.UID, workload.Generation = "owned", 1
					if test.createErr != nil && !test.lostResponse {
						return true, nil, test.createErr
					}
					require.NoError(t, client.Tracker().Create(resource, workload, workload.Namespace))
					if test.lostResponse {
						return true, nil, test.createErr
					}
					return true, workload, nil
				})
				client.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
					service := action.(ktesting.CreateAction).GetObject().(*corev1.Service)
					assert.Equal(t, workload.Name, service.Name)
					assert.Equal(t, corev1.ClusterIPNone, service.Spec.ClusterIP)
					assert.Equal(t, workload.Spec.Selector.MatchLabels, service.Spec.Selector)
					assert.Equal(t, []metav1.OwnerReference{*metav1.NewControllerRef(workload, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}, service.OwnerReferences)
					if test.serviceErr != nil {
						return true, nil, test.serviceErr
					}
					return false, nil, nil
				})
				reads, cleanupReads := 0, 0
				deleted := false
				client.PrependReactor("get", "statefulsets", func(action ktesting.Action) (bool, runtime.Object, error) {
					if test.createErr != nil && !test.lostResponse {
						return false, nil, nil
					}
					if deleted {
						cleanupReads++
						if cleanupReads > 1 {
							return false, nil, nil
						}
					}
					reads++
					current := workload.DeepCopy()
					if test.replaced {
						current.UID = "replacement"
					}
					current.Status = appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 3}
					if reads == 1 || reads == 4 {
						current.Status.ReadyReplicas = 2
					}
					if reads == 2 {
						current.Status.ObservedGeneration = 0
					}
					if reads == test.cancelAt {
						cancel()
					}
					return true, current, nil
				})
				client.PrependReactor("delete", "statefulsets", func(action ktesting.Action) (bool, runtime.Object, error) {
					deleted = true
					opts := action.(ktesting.DeleteAction).GetDeleteOptions()
					assert.Equal(t, &metav1.Preconditions{UID: &workload.UID}, opts.Preconditions)
					require.NotNil(t, opts.PropagationPolicy)
					assert.Equal(t, metav1.DeletePropagationForeground, *opts.PropagationPolicy)
					if test.cleanupErr != nil {
						return true, nil, test.cleanupErr
					}
					return false, nil, nil
				})

				start := time.Now()
				err = g.run(ctx, client.AppsV1(), client.CoreV1())
				if test.wantErr == "" {
					assert.NoError(t, err)
					assert.Equal(t, 5*time.Second, time.Since(start), "duration starts at readiness and completion waits for recovery and deletion")
				} else {
					require.ErrorContains(t, err, test.wantErr)
				}
				assert.Equal(t, test.wantDelete, deleted)
				if test.cleanupErr != nil {
					assert.ErrorIs(t, err, test.cleanupErr, "cleanup error returned")
				} else if deleted {
					assert.Equal(t, 2, cleanupReads, "wait for foreground deletion")
					_, getErr := client.Tracker().Get(resource, workload.Namespace, workload.Name)
					assert.True(t, apierrors.IsNotFound(getErr), "StatefulSet removed before returning")
				}
				if ctx.Err() != nil {
					assert.ErrorIs(t, err, ctx.Err())
					assert.NotContains(t, err.Error(), "cleanup", "cleanup survives run cancellation")
				}
				for _, action := range client.Actions() {
					assert.Equal(t, "workloads", action.GetNamespace())
				}
			})
		})
	}
}

func TestStatefulSetCleanupOwnership(t *testing.T) {
	for _, test := range []struct {
		name  string
		owner string
		uid   types.UID
	}{
		{name: "replaced", owner: "owned", uid: "replacement"},
		{name: "foreign owner", owner: "other", uid: "uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "workloads", UID: "uid"}}
			current := workload.DeepCopy()
			current.UID = test.uid
			current.Labels = map[string]string{generatorOwnerLabel: test.owner}
			client := fake.NewClientset(current)
			err := cleanupStatefulSet(t.Context(), client.AppsV1().StatefulSets(workload.Namespace), workload)
			assert.ErrorContains(t, err, "refuse to delete unowned")
			for _, action := range client.Actions() {
				assert.NotEqual(t, "delete", action.GetVerb())
			}
		})
	}
}

func TestStatefulSetCleanupWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "uid", Labels: map[string]string{generatorOwnerLabel: "owned"}}}
		client := fake.NewClientset(workload)
		client.PrependReactor("delete", "statefulsets", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), statefulSetCleanupTimeout)
		defer cancel()
		err := cleanupStatefulSet(ctx, client.AppsV1().StatefulSets(workload.Namespace), workload)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestStatefulSetGeneratorCount(t *testing.T) {
	for _, failCreate := range []bool{false, true} {
		name := "complete"
		if failCreate {
			name = "partial create"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, err := NewStatefulSetGenerator(parseSpec(t, "count: 2\n"+statefulSetTestSpec), "/does/not/exist")
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				client := fake.NewClientset()
				resource := appsv1.SchemeGroupVersion.WithResource("statefulsets")
				createErr := errors.New("create rejected")
				var created []*appsv1.StatefulSet
				attempts := 0
				client.PrependReactor("create", "statefulsets", func(action ktesting.Action) (bool, runtime.Object, error) {
					attempts++
					if failCreate && attempts == 2 {
						return true, nil, createErr
					}
					set := action.(ktesting.CreateAction).GetObject().(*appsv1.StatefulSet).DeepCopy()
					set.UID = types.UID(set.Name)
					created = append(created, set)
					return true, set, client.Tracker().Create(resource, set, set.Namespace)
				})
				client.PrependReactor("get", "statefulsets", func(action ktesting.Action) (bool, runtime.Object, error) {
					obj, err := client.Tracker().Get(resource, action.GetNamespace(), action.(ktesting.GetAction).GetName())
					if err != nil {
						return true, nil, err
					}
					set := obj.(*appsv1.StatefulSet)
					if !failCreate && len(created) == g.config.Count {
						set.Status.ReadyReplicas = *set.Spec.Replicas
					}
					return true, set, nil
				})

				err = g.run(ctx, client.AppsV1(), client.CoreV1())
				if failCreate {
					assert.ErrorIs(t, err, createErr)
					assert.NotEmpty(t, created)
				} else {
					assert.NoError(t, err)
					require.Len(t, created, 2, "both sets started before readiness")
					assert.NotEqual(t, created[0].Name, created[1].Name)
					for _, set := range created {
						assert.Equal(t, int32(3), *set.Spec.Replicas)
						service, err := client.CoreV1().Services(set.Namespace).Get(t.Context(), set.Name, metav1.GetOptions{})
						require.NoError(t, err)
						assert.Equal(t, set.Spec.Selector.MatchLabels, service.Spec.Selector)
						assert.True(t, metav1.IsControlledBy(service, set), "each set owns its Service for garbage collection")
					}
				}
				remaining, err := client.AppsV1().StatefulSets("workloads").List(t.Context(), metav1.ListOptions{})
				require.NoError(t, err)
				assert.Empty(t, remaining.Items, "every started StatefulSet is cleaned up")
			})
		})
	}
}

func TestStatefulSetReadFailures(t *testing.T) {
	resource := schema.GroupResource{Group: "apps", Resource: "statefulsets"}
	goaway := errors.New("http2: server sent GOAWAY and closed the connection; LastStreamID=855, ErrCode=NO_ERROR")
	for _, test := range []struct {
		name      string
		failures  []error
		wantReads int
		wantErr   bool
	}{
		{name: "server timeout recovers", failures: []error{apierrors.NewTimeoutError("busy", 1)}, wantReads: 2},
		{name: "server timeout response recovers", failures: []error{apierrors.NewServerTimeout(resource, "get", 1)}, wantReads: 2},
		{name: "overload recovers", failures: []error{apierrors.NewTooManyRequests("busy", 1), apierrors.NewServiceUnavailable("busy")}, wantReads: 3},
		{name: "three failures recover", failures: []error{apierrors.NewServiceUnavailable("busy"), apierrors.NewServiceUnavailable("busy"), apierrors.NewServiceUnavailable("busy")}, wantReads: 4},
		{name: "budget resets after success", failures: []error{apierrors.NewServiceUnavailable("busy"), nil, apierrors.NewServiceUnavailable("busy"), apierrors.NewServiceUnavailable("busy")}, wantReads: 5},
		{name: "GOAWAY recovers", failures: []error{goaway}, wantReads: 2},
		{name: "unexpected EOF recovers", failures: []error{io.ErrUnexpectedEOF}, wantReads: 2},
		{name: "three GOAWAY failures recover", failures: []error{goaway, goaway, goaway}, wantReads: 4},
		{name: "GOAWAY budget resets after success", failures: []error{goaway, nil, goaway, goaway}, wantReads: 5},
		{name: "unauthorized GOAWAY stops", failures: []error{apierrors.NewUnauthorized(goaway.Error())}, wantReads: 1, wantErr: true},
		{name: "forbidden GOAWAY stops", failures: []error{apierrors.NewForbidden(resource, "owned", goaway)}, wantReads: 1, wantErr: true},
		{name: "VPN route failure stops", failures: []error{errors.New("no route to host")}, wantReads: 1, wantErr: true},
		{name: "unauthorized stops", failures: []error{apierrors.NewUnauthorized("expired")}, wantReads: 1, wantErr: true},
		{name: "forbidden stops", failures: []error{apierrors.NewForbidden(resource, "owned", errors.New("denied"))}, wantReads: 1, wantErr: true},
		{name: "request deadline recovers", failures: []error{&url.Error{Op: "Get", URL: "https://example.invalid", Err: context.DeadlineExceeded}}, wantReads: 2},
		{name: "response body timeout recovers", failures: []error{fmt.Errorf("unexpected error when reading response body. Please retry. Original error: %w", context.DeadlineExceeded)}, wantReads: 2},
		{name: "transport timeout recovers", failures: []error{&net.DNSError{Err: "timeout", IsTimeout: true}}, wantReads: 2},
		{name: "missing stops", failures: []error{apierrors.NewNotFound(resource, "owned")}, wantReads: 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				owned := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "nktp", UID: "original"}, Spec: appsv1.StatefulSetSpec{Replicas: new(int32(1))}}
				client := fake.NewClientset()
				reads := 0
				client.PrependReactor("get", "statefulsets", func(ktesting.Action) (bool, runtime.Object, error) {
					reads++
					if reads <= len(test.failures) && test.failures[reads-1] != nil {
						return true, nil, test.failures[reads-1]
					}
					current := owned.DeepCopy()
					if reads > len(test.failures) {
						current.Status.ReadyReplicas = 1
					}
					return true, current, nil
				})
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				err := waitForStatefulSet(ctx, client.AppsV1().StatefulSets("nktp"), owned, 0)
				assert.Equal(t, test.wantReads, reads)
				if test.wantErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				for _, action := range client.Actions() {
					assert.Equal(t, "get", action.GetVerb())
				}
			})
		})
	}
}
