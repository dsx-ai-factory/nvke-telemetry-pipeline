// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/require"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

// TestNodelessMetrics proves that a Probe labeled for the nodeless pool is
// scraped through the blackbox exporter, by that pool only. The node
// collector's per-node allocation leaves such targets unassigned.
func TestNodelessMetrics(t *testing.T) {
	namespace := testenv.EnvConf().Namespace()
	job := fmt.Sprintf("probe/%s/nktp-e2e-nodeless-probe", namespace)
	f := features.New("Nodeless metrics").
		Setup(funcs.ApplyResources(FieldManager, "manifests/nodeless-metrics", "*.yaml", decoder.MutateNamespace(namespace))).
		Assess("A labeled Probe is scraped by the nodeless pool only", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := client.Services(namespace)
			// Every fresh, successful sample of the Probe, grouped by the pool that sent it.
			metric := fmt.Sprintf(`probe_success{job=%q}`, job)
			query := fmt.Sprintf(`count by (nktp_metrics_pool) ((%s == 1) and (timestamp(%s) > time() - %d))`,
				metric, metric, int(componentMetricMaxAge.Seconds()))
			var got []string
			var lastErr error
			err = wait.For(func(ctx context.Context) (bool, error) {
				got, lastErr = prometheusMetricLabelValues(ctx, services, query, "nktp_metrics_pool")
				if lastErr != nil {
					return false, nil
				}
				return len(got) == 1 && got[0] == "nodeless", nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err,
				"want probe_success == 1 for job %s from pool [nodeless]; got pools %v; last query error: %v",
				job, got, lastErr)

			// The node collector sets no pool label, which the query above cannot
			// see. Any other series of this job is a second scrape.
			others := fmt.Sprintf(`count by (job) (probe_success{job=%q,nktp_metrics_pool!="nodeless"})`, job)
			duplicates, err := prometheusMetricLabelValues(ctx, services, others, "job")
			require.NoError(t, err)
			require.Empty(t, duplicates, "job %s is also scraped outside the nodeless pool", job)
			return ctx
		}).
		Teardown(funcs.DeleteResources(FieldManager, "manifests/nodeless-metrics", "*.yaml", decoder.MutateNamespace(namespace))).
		Feature()
	testenv.Test(t, f)
}
