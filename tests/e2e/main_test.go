// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/config"
	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/kube"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var testenv = config.NewEnvironmentFromFlags()

const ReleaseName = "nktp-e2e"
const FieldManager = "nktp-e2e"

func TestMain(t *testing.M) {
	// Match the Helm CLI instead of recording the test binary as the field manager.
	kube.ManagedFieldsManager = "helm"

	cfg, err := envconf.NewFromFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error while creating env config: %v\n", err)
		os.Exit(1)
	}
	if cfg.KubeconfigFile() == "" {
		fmt.Fprintln(os.Stderr, "pass -kubeconfig explicitly, or run task e2e:test")
		os.Exit(1)
	}
	if cfg.Namespace() != "" {
		testenv.ReleaseNamespace = cfg.Namespace()
	}
	testenv.SetEnvironment(env.NewWithConfig(cfg))
	testenv.Install()
	testenv.Setup(waitForCollectors)
	testenv.Uninstall()
	os.Exit(testenv.Run(t))
}

func waitForCollectors(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	namespace := cfg.Namespace()
	client := cfg.Client()
	collectors := &unstructured.UnstructuredList{}
	collectors.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "opentelemetry.io",
		Version: "v1beta1",
		Kind:    "OpenTelemetryCollectorList",
	})
	if err := client.Resources(namespace).List(ctx, collectors); err != nil {
		return ctx, fmt.Errorf("listing Collector resources: %w", err)
	}
	if len(collectors.Items) == 0 {
		return ctx, fmt.Errorf("no Collector resources found in namespace %s", namespace)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	targetAllocators := 0
	for _, collector := range collectors.Items {
		mode, found, err := unstructured.NestedString(collector.Object, "spec", "mode")
		if err != nil {
			return ctx, fmt.Errorf("reading mode for Collector %s: %w", collector.GetName(), err)
		}
		if !found {
			return ctx, fmt.Errorf("Collector %s does not specify a mode", collector.GetName())
		}

		name := collector.GetName() + "-collector"
		switch mode {
		case "deployment":
			err = funcs.WaitForDeployment(waitCtx, funcs.DeploymentWaitOptions{Name: name, Namespace: namespace, Client: client})
		case "daemonset":
			err = funcs.WaitForDaemonSet(waitCtx, funcs.DaemonSetWaitOptions{Name: name, Namespace: namespace, Client: client})
		case "statefulset":
			err = funcs.WaitForStatefulSet(waitCtx, funcs.StatefulSetWaitOptions{Name: name, Namespace: namespace, Client: client})
		default:
			return ctx, fmt.Errorf("Collector %s has unsupported mode %q", collector.GetName(), mode)
		}
		if err != nil {
			return ctx, fmt.Errorf("waiting for %s Collector workload %s/%s: %w", mode, namespace, name, err)
		}

		enabled, found, err := unstructured.NestedBool(collector.Object, "spec", "targetAllocator", "enabled")
		if err != nil {
			return ctx, fmt.Errorf("reading Target Allocator configuration for Collector %s: %w", collector.GetName(), err)
		}
		if !found || !enabled {
			continue
		}
		targetAllocators++
		name = collector.GetName() + "-targetallocator"
		if err := funcs.WaitForDeployment(waitCtx, funcs.DeploymentWaitOptions{Name: name, Namespace: namespace, Client: client}); err != nil {
			return ctx, fmt.Errorf("waiting for Target Allocator Deployment %s/%s: %w", namespace, name, err)
		}
	}
	if targetAllocators == 0 {
		return ctx, fmt.Errorf("no Collectors have a Target Allocator enabled")
	}
	return ctx, nil
}

// TestStableReapply checks that identical Helm inputs preserve workload
// generations, reports changes to owned workloads, and waits for Collectors
// to settle. It runs serially because it upgrades the shared release.
func TestStableReapply(t *testing.T) {
	settled := false
	f := features.New("Stable Helm reapply").WithLabel("execution", "serial").
		Assess("Identical inputs preserve Helm-managed generations", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			before, err := funcs.ListWorkloads(waitCtx, c.Client(), c.Namespace())
			require.NoError(t, err)
			generations := func(workloads []funcs.Workload, hasOwner bool) map[string]int64 {
				result := map[string]int64{}
				for _, workload := range workloads {
					if workload.HasOwner == hasOwner {
						result[workload.String()] = workload.Generation
					}
				}
				return result
			}
			wasHelm := generations(before, false)
			require.NotEmpty(t, wasHelm, "the release must contain Helm-managed workloads")
			chart, err := loader.Load(testenv.ChartPath)
			require.NoError(t, err)
			values, err := common.ReadValuesFile(testenv.ValuesPath)
			require.NoError(t, err)
			getter := &genericclioptions.ConfigFlags{
				KubeConfig: new(c.KubeconfigFile()),
				Context:    new(c.KubeContext()),
				Namespace:  new(c.Namespace()),
			}
			helmcfg := action.NewConfiguration()
			require.NoError(t, helmcfg.Init(getter, c.Namespace(), "secret"), "creating Helm upgrade config")
			upgrade := action.NewUpgrade(helmcfg)
			upgrade.Namespace = c.Namespace()
			upgrade.ResetValues = true
			upgrade.WaitStrategy = kube.StatusWatcherStrategy
			upgrade.WaitForJobs = true
			upgrade.Timeout = 15 * time.Minute
			_, err = upgrade.RunWithContext(waitCtx, testenv.ReleaseName, chart, values)
			require.NoError(t, err, "reapplying identical chart inputs")
			_, err = waitForCollectors(waitCtx, c)
			require.NoError(t, err, "waiting for Collector workloads after reapply")
			settled = true
			after, err := funcs.ListWorkloads(waitCtx, c.Client(), c.Namespace())
			require.NoError(t, err)
			assert.Equal(t, wasHelm, generations(after, false), "identical inputs changed a Helm-managed workload")
			if !assert.ObjectsAreEqual(generations(before, true), generations(after, true)) {
				t.Log("the Operator changed workloads derived from unchanged Collector resources")
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if !settled {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute+30*time.Second)
				defer cancel()
				_, err := waitForCollectors(cleanupCtx, c)
				require.NoError(t, err, "settling Collector workloads after reapply")
			}
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
