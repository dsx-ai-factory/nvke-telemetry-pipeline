// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package funcs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const defaultInterval = 500 * time.Millisecond

func ApplyResources(manager, dir, pattern string, options ...decoder.DecodeOption) features.Func {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Helper()

		dfs := os.DirFS(dir)

		files, _ := fs.Glob(dfs, pattern)
		if len(files) == 0 {
			t.Errorf("No resources found in %s", filepath.Join(dir, pattern))
			return ctx
		}

		if err := decoder.DecodeEachFile(ctx, dfs, pattern, ApplyHandler(c.Client().Resources(), manager), options...); err != nil {
			t.Fatal(err)
			return ctx
		}

		t.Logf("Applied resources from %s (matched %d manifests)", filepath.Join(dir, pattern), len(files))
		return ctx
	}
}

func ApplyHandler(r *resources.Resources, manager string) decoder.HandlerFunc {
	return func(ctx context.Context, obj k8s.Object) error {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return fmt.Errorf("converting %T to unstructured: %w", obj, err)
		}

		u := &unstructured.Unstructured{Object: data}
		cfg := crclient.ApplyConfigurationFromUnstructured(u)

		if err := r.GetControllerRuntimeClient().Apply(
			ctx, cfg, crclient.FieldOwner(manager),
		); err != nil {
			return fmt.Errorf("applying %s %s/%s: %w",
				u.GetKind(), u.GetNamespace(), u.GetName(), err)
		}
		return nil
	}
}

func DeleteResources(manager, dir, pattern string, options ...decoder.DecodeOption) features.Func {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Helper()

		dfs := os.DirFS(dir)

		// TODO(maxcelant): Add propagation policy parameter later if needed
		if err := decoder.DecodeEachFile(ctx, dfs, pattern, decoder.DeleteHandler(c.Client().Resources(), resources.WithDeletePropagation("Foreground")), options...); err != nil {
			t.Fatal(err)
			return ctx
		}

		files, _ := fs.Glob(dfs, pattern)
		t.Logf("Deleted resources from %s (matched %d manifests)", filepath.Join(dir, pattern), len(files))

		return ctx
	}
}

func DaemonsetReady(name string) features.Func {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.Namespace()}}
		err := wait.For(
			conditions.New(c.Client().Resources()).DaemonSetReady(ds),
			wait.WithTimeout(workloadWaitTimeout),
			wait.WithInterval(defaultInterval),
			wait.WithImmediate(),
		)
		require.NoError(t, err, "waiting for DaemonSet %s/%s to be ready on all nodes", c.Namespace(), name)
		return ctx
	}
}

func CreateNamespace(name string) features.Func {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		ctx, err := envfuncs.CreateNamespace(name)(ctx, c)
		require.NoError(t, err, "creating namespace %s", name)
		return ctx
	}
}

func DeleteNamespace(name string) features.Func {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		ctx, err := envfuncs.DeleteNamespace(name)(ctx, c)
		require.NoError(t, err, "deleting namespace %s", name)
		return ctx
	}
}
