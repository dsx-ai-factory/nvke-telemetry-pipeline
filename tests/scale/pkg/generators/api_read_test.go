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
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	ktesting "k8s.io/client-go/testing"
)

func TestRetryAPIRead(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{name: "success"},
		{name: "throttled", err: apierrors.NewTooManyRequests("busy", 0), retry: true},
		{name: "internal error", err: apierrors.NewInternalError(errors.New("server failed")), retry: true},
		{name: "unavailable", err: apierrors.NewServiceUnavailable("restarting"), retry: true},
		{name: "bad gateway", err: apierrors.NewGenericServerResponse(502, "get", schema.GroupResource{Resource: "pods"}, "test", "proxy failed", 0, false), retry: true},
		{name: "API timeout", err: apierrors.NewTimeoutError("timed out", 0), retry: true},
		{name: "server timeout", err: apierrors.NewServerTimeout(schema.GroupResource{Resource: "pods"}, "get", 0), retry: true},
		{name: "request timeout", err: &url.Error{Op: "Get", URL: "https://example.invalid", Err: context.DeadlineExceeded}, retry: true},
		{name: "response body timeout", err: fmt.Errorf("unexpected error when reading response body. Please retry. Original error: %w", context.DeadlineExceeded), retry: true},
		{name: "HTTP2 GOAWAY", err: errors.New("http2: server sent GOAWAY and closed the connection"), retry: true},
		{name: "wrapped HTTP2 internal error", err: fmt.Errorf("read response body: %w", http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}), retry: true},
		{name: "wrapped HTTP2 protocol error with transport message", err: fmt.Errorf("read response body: %w", http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol, Cause: errors.New("connection reset by peer")})},
		{name: "network timeout", err: &net.DNSError{Err: "timed out", IsTimeout: true}, retry: true},
		{name: "connection reset", err: fmt.Errorf("read: %w", syscall.ECONNRESET), retry: true},
		{name: "connection refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), retry: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, retry: true},
		{name: "wrapped EOF", err: fmt.Errorf("read response body: %w", io.EOF), retry: true},
		{name: "wrapped unexpected EOF", err: fmt.Errorf("read response body: %w", io.ErrUnexpectedEOF), retry: true},
		{name: "unauthorized", err: apierrors.NewUnauthorized("credentials")},
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "test", errors.New("denied"))},
		{name: "forbidden transport message", err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "test", errors.New("connection reset by peer"))},
		{name: "bad request", err: apierrors.NewBadRequest("invalid")},
		{name: "not found", err: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "test")},
		{name: "canceled", err: context.Canceled},
		{name: "other error", err: errors.New("permanent failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				started := time.Now()
				_, err := retryAPIRead(t.Context(), func(context.Context) (int, error) {
					attempts++
					return 0, test.err
				})
				assert.ErrorIs(t, err, test.err)
				if test.retry {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
					assert.Equal(t, apiReadRecoveryTimeout, time.Since(started))
					assert.Greater(t, attempts, 3)
					assert.Less(t, attempts, 25, "backoff must prevent a busy retry loop")
				} else {
					assert.Equal(t, test.err, err, "terminal errors must not be wrapped as recovery failures")
					assert.Equal(t, 1, attempts)
					assert.Zero(t, time.Since(started))
				}
			})
		})
	}
}

func TestRetryAPIReadCancellation(t *testing.T) {
	for _, mode := range []string{"already canceled", "cancel during wait", "deadline during wait"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wantErr := context.Canceled
				wantAttempts := 1
				switch mode {
				case "already canceled":
					cancel()
					wantAttempts = 0
				case "cancel during wait":
					timer := time.AfterFunc(time.Second/2, cancel)
					defer timer.Stop()
				case "deadline during wait":
					ctx, cancel = context.WithTimeout(ctx, time.Second/2)
					defer cancel()
					wantErr = context.DeadlineExceeded
				}
				attempts := 0
				_, err := retryAPIRead(ctx, func(context.Context) (int, error) {
					attempts++
					return 0, apierrors.NewServiceUnavailable("try later")
				})
				assert.ErrorIs(t, err, wantErr)
				assert.Equal(t, wantAttempts, attempts)
			})
		})
	}
}

// TestRetryAPIReadRecovery verifies that failures can recover beyond the old
// three-attempt limit and that separate calls receive independent budgets.
func TestRetryAPIReadRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for range 2 {
			attempts := 0
			value, err := retryAPIRead(t.Context(), func(context.Context) (int, error) {
				attempts++
				if attempts <= 4 {
					return 0, context.DeadlineExceeded
				}
				return 42, nil
			})
			require.NoError(t, err)
			assert.Equal(t, 42, value)
			assert.Equal(t, 5, attempts)
			time.Sleep(apiReadRecoveryTimeout)
		}
	})
}

// TestRetryAPIReadInFlight checks that an outstanding request is bounded by the
// recovery deadline or the parent's earlier cancellation, not just the retry sleeps.
func TestRetryAPIReadInFlight(t *testing.T) {
	for _, mode := range []string{"recovery deadline", "parent cancellation", "parent deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				wantErr := context.DeadlineExceeded
				wantElapsed := apiReadRecoveryTimeout
				switch mode {
				case "parent cancellation":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					timer := time.AfterFunc(5*time.Second, cancel)
					defer timer.Stop()
					wantElapsed, wantErr = 5*time.Second, context.Canceled
				case "parent deadline":
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					wantElapsed = 5 * time.Second
				}
				started := time.Now()
				attempts := 0
				unavailable := apierrors.NewServiceUnavailable("try later")
				_, err := retryAPIRead(ctx, func(ctx context.Context) (int, error) {
					attempts++
					if attempts == 1 {
						return 0, unavailable
					}
					<-ctx.Done()
					return 0, ctx.Err()
				})
				assert.ErrorIs(t, err, wantErr)
				assert.Equal(t, wantElapsed, time.Since(started))
				assert.Equal(t, 2, attempts)
				if mode == "recovery deadline" {
					assert.ErrorIs(t, err, unavailable, "retain the last API error")
				}
			})
		})
	}
}

// TestRetryAPIReadDeadlineResult preserves completed reads and terminal errors
// that arrive at the recovery deadline. Parent cancellation still takes priority.
func TestRetryAPIReadDeadlineResult(t *testing.T) {
	forbidden := apierrors.NewForbidden(corev1.Resource("pods"), "test", errors.New("denied"))
	for _, test := range []struct {
		name         string
		readErr      error
		cancelParent bool
		wantErr      error
	}{
		{name: "success"},
		{name: "terminal error", readErr: forbidden, wantErr: forbidden},
		{name: "parent canceled", cancelParent: true, wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				value, err := retryAPIRead(ctx, func(ctx context.Context) (int, error) {
					<-ctx.Done()
					if test.cancelParent {
						cancel()
					}
					return 42, test.readErr
				})
				assert.Equal(t, test.wantErr, err)
				if err == nil {
					assert.Equal(t, 42, value)
				}
			})
		})
	}
}

func TestPodReadRecoveryExhaustion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset()
		unavailable := apierrors.NewServiceUnavailable("offline")
		client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, unavailable
		})
		started := time.Now()
		err := waitForPods(t.Context(), client.CoreV1().Pods("nktp"), "owner", nil)
		assert.ErrorIs(t, err, unavailable)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, apiReadRecoveryTimeout, time.Since(started))
		for _, action := range client.Actions() {
			assert.Equal(t, "list", action.GetVerb(), "failed observation must not delete workloads")
		}
	})
}

// TestCleanupPodsRecoveryDeadline ensures that repeated successful recovery windows
// cannot extend the caller's five-minute cleanup deadline.
func TestCleanupPodsRecoveryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var owned []*corev1.Pod
		client := fake.NewClientset()
		for i := range 3 {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint(i), Labels: map[string]string{generatorOwnerLabel: "owner"}}}
			created, err := client.CoreV1().Pods("nktp").Create(t.Context(), pod, metav1.CreateOptions{})
			require.NoError(t, err)
			owned = append(owned, created)
		}
		pods := &slowRecoveryPods{PodInterface: client.CoreV1().Pods("nktp"), reads: make(map[string]int)}
		ctx, cancel := context.WithTimeout(t.Context(), podCleanupTimeout)
		defer cancel()
		started := time.Now()
		err := cleanupPods(ctx, pods, "owner", owned)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, podCleanupTimeout, time.Since(started))
		remaining, err := client.CoreV1().Pods("nktp").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, remaining.Items, 1, "do not delete a Pod whose ownership read was canceled")
		assert.Equal(t, "2", remaining.Items[0].Name)
	})
}

// slowRecoveryPods simulates a transient failure followed by a slow successful GET.
type slowRecoveryPods struct {
	coreclient.PodInterface
	reads map[string]int
}

func (p *slowRecoveryPods) Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Pod, error) {
	p.reads[name]++
	if p.reads[name] == 1 {
		return nil, apierrors.NewServiceUnavailable("recovering")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(100 * time.Second):
		return p.PodInterface.Get(ctx, name, opts)
	}
}
