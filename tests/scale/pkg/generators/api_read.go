// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package generators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"golang.org/x/net/http2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	netutil "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
)

// apiReadRecoveryTimeout bounds a read and its retries, including request time.
const apiReadRecoveryTimeout = 2 * time.Minute

// retryAPIRead retries transient GET/LIST failures with exponential backoff and
// jitter. Each call has its own recovery deadline, bounded by the caller's context.
// Writes and workload checks remain the caller's responsibility.
func retryAPIRead[T any](ctx context.Context, read func(context.Context) (T, error)) (T, error) {
	readCtx, cancel := context.WithTimeout(ctx, apiReadRecoveryTimeout)
	defer cancel()
	started := time.Now()
	delay := wait.Backoff{
		Duration: time.Second,
		Factor:   2,
		Jitter:   0.2,
		Steps:    4, // DelayFunc keeps retrying after the delay reaches the cap.
		Cap:      10 * time.Second,
	}.DelayFunc()
	var value T
	var lastErr error
	completed := false
	err := delay.Until(readCtx, true, true, func(readCtx context.Context) (bool, error) {
		// Until invokes the first condition even when the context is already done.
		if err := readCtx.Err(); err != nil {
			return false, err
		}
		var err error
		value, err = read(readCtx)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err == nil || !transientAPIReadError(err) {
			completed = true
			if err == nil && lastErr != nil {
				slog.InfoContext(ctx, "Kubernetes API read recovered", "elapsed", time.Since(started))
			}
			return true, err
		}
		if lastErr == nil {
			slog.WarnContext(ctx, "Kubernetes API read delayed", "error", err, "recovery_timeout", apiReadRecoveryTimeout)
		}
		// Preserve the API error if the recovery deadline interrupts a retry.
		if lastErr == nil || !errors.Is(err, readCtx.Err()) {
			lastErr = err
		}
		return false, nil
	})
	// Preserve completed reads and terminal errors at the recovery deadline.
	if completed {
		return value, err
	}
	if ctx.Err() != nil {
		var zero T
		return zero, ctx.Err()
	}
	var zero T
	return zero, fmt.Errorf("API read did not recover within %s: %w", apiReadRecoveryTimeout, errors.Join(err, lastErr))
}

// transientAPIReadError identifies retryable API and transport errors. API status
// takes precedence so authentication, invalid requests and NotFound stay terminal.
func transientAPIReadError(err error) bool {
	if apierrors.IsTooManyRequests(err) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return true
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := status.Status().Code
		return code >= 500 && code < 600
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) {
		return streamErr.Code == http2.ErrCodeInternal
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return (errors.As(err, &networkErr) && networkErr.Timeout()) ||
		netutil.IsConnectionReset(err) || netutil.IsConnectionRefused(err) ||
		netutil.IsHTTP2ConnectionLost(err) || netutil.IsProbableEOF(err)
}
