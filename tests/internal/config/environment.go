// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/storage/driver"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

type Environment struct {
	env.Environment

	ReleaseName, ReleaseNamespace string
	ChartPath, ValuesPath         string
	KeepRelease                   bool

	helmcfg *action.Configuration
}

func NewEnvironmentFromFlags() *Environment {
	env := &Environment{
		ReleaseName:      "nktp-e2e",
		ReleaseNamespace: "nktp",
		ChartPath:        "../../nktp",
		ValuesPath:       "values.yaml",
		KeepRelease:      false,
	}
	flag.StringVar(&env.ReleaseName, "release-name", env.ReleaseName, "Helm release to install or reuse")
	flag.StringVar(&env.ChartPath, "chart", env.ChartPath, "Path to the Helm chart under test")
	flag.StringVar(&env.ValuesPath, "values", env.ValuesPath, "Path to Helm values.yaml file")
	flag.BoolVar(&env.KeepRelease, "keep-release", env.KeepRelease, "Keep the existing deployed Helm release")
	return env
}

func (e *Environment) SetEnvironment(env env.Environment) {
	e.Environment = env
}

func (e *Environment) Install() {
	e.Setup(func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		cfg.WithNamespace(e.ReleaseNamespace)
		getter := &genericclioptions.ConfigFlags{
			KubeConfig: new(cfg.KubeconfigFile()),
			Namespace:  new(cfg.Namespace()),
		}
		e.helmcfg = action.NewConfiguration()
		err := e.helmcfg.Init(getter, cfg.Namespace(), "secret")
		if err != nil {
			return ctx, fmt.Errorf("failed to create helm install config: %w", err)
		}
		if e.KeepRelease {
			metadata, err := action.NewGetMetadata(e.helmcfg).Run(e.ReleaseName)
			if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
				return ctx, fmt.Errorf("getting helm metadata: %w", err)
			}
			if err == nil && metadata.Status == "deployed" {
				slog.Info("reusing the existing deployed helm release, skipping install", "release", e.ReleaseName, "namespace", e.ReleaseNamespace)
				return ctx, nil
			}
		}
		slog.Info("cleaning up any existing deployed chart")
		if err := e.cleanup(ctx, cfg); err != nil {
			return ctx, fmt.Errorf("cleaning up helm chart in target cluster: %w", err)
		}
		chart, err := loader.Load(e.ChartPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to load chart at target path %s: %w", e.ChartPath, err)
		}
		values, err := common.ReadValuesFile(e.ValuesPath)
		if err != nil {
			return ctx, fmt.Errorf("failed to load values file at target path %s: %w", e.ValuesPath, err)
		}
		install := action.NewInstall(e.helmcfg)
		install.ReleaseName = e.ReleaseName
		install.Namespace = e.ReleaseNamespace
		install.CreateNamespace = true
		install.WaitStrategy = kube.StatusWatcherStrategy
		install.Timeout = 5 * time.Minute
		install.WaitForJobs = true
		slog.Info("installing chart on target cluster", "release", e.ReleaseName, "namespace", e.ReleaseNamespace)
		if _, err = install.RunWithContext(ctx, chart, values); err != nil {
			return ctx, fmt.Errorf("failed to helm install: %w", err)
		}
		slog.Info("setup complete, starting suite")
		return ctx, nil
	})

}

func (e *Environment) Uninstall() {
	if e.KeepRelease {
		slog.Info("helm chart won't be uninstalled upon completion", "release", e.ReleaseName, "namespace", e.ReleaseNamespace)
		return
	}
	e.Finish(func(ctx context.Context, c *envconf.Config) (context.Context, error) {
		if e.helmcfg == nil {
			panic("helm config is not set")
		}
		slog.Info("uninstalling chart from target cluster")
		if err := e.cleanup(ctx, c); err != nil {
			return ctx, fmt.Errorf("uninstalling chart: %w", err)
		}
		return ctx, nil
	})
}

func (e *Environment) cleanup(ctx context.Context, cfg *envconf.Config) error {
	releaseNamespace := cfg.Namespace()
	uninstall := action.NewUninstall(e.helmcfg)
	uninstall.Timeout = time.Minute * 5
	uninstall.WaitStrategy = kube.StatusWatcherStrategy
	uninstall.IgnoreNotFound = true
	if _, err := uninstall.Run(e.ReleaseName); err != nil {
		return fmt.Errorf("uninstalling chart: %w", err)
	}
	client, err := cfg.NewClient()
	if err != nil {
		return fmt.Errorf("creating client for lease cleanup: %w", err)
	}
	resources := client.Resources(releaseNamespace)
	var leases coordinationv1.LeaseList
	if err := resources.List(ctx, &leases); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing leases in %s: %w", releaseNamespace, err)
	}
	for _, lease := range leases.Items {
		if !strings.HasSuffix(lease.Name, ".opentelemetry.io") {
			continue
		}
		if err := resources.Delete(ctx, &lease); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting lease %s/%s: %w", releaseNamespace, lease.Name, err)
		}
	}
	return nil

}
