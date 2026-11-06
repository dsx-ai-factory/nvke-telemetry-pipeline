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

func TestNodelessMetrics(t *testing.T) {
	const manifests = "manifests/nodeless-metrics"
	namespace := testenv.EnvConf().Namespace()
	job := fmt.Sprintf("probe/%s/nktp-e2e-nodeless-probe", namespace)
	var started time.Time
	f := features.NewWithDescription("NodelessMetrics",
		"An unlabeled Probe has no node, so the nodeless pool scrapes it through the blackbox exporter and the node collector does not.").
		WithSetup("ApplyingUnlabeledProbe", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			started = time.Now()
			return funcs.ApplyResources(FieldManager, manifests, "*.yaml", decoder.MutateNamespace(namespace))(ctx, t, c)
		}).
		AssessWithDescription("ProbeScrapedByNodelessPoolOnly", "The Probe's samples come from the nodeless pool and from no other pool.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			client, err := coreclient.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err, "creating the Service proxy client")
			services := client.Services(namespace)
			// Every successful sample of the Probe scraped after this run applied
			// it, grouped by the pool that sent it. A series left by an earlier
			// run of the same job cannot match.
			metric := fmt.Sprintf(`probe_success{job=%q}`, job)
			query := fmt.Sprintf(`count by (nktp_metrics_pool) ((%s == 1) and (timestamp(%s) > %d))`,
				metric, metric, started.Unix())
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
			queryCtx, cancelQuery := context.WithTimeout(ctx, time.Minute)
			defer cancelQuery()
			duplicates, err := prometheusMetricLabelValues(queryCtx, services, others, "job")
			require.NoError(t, err)
			require.Empty(t, duplicates, "job %s is also scraped outside the nodeless pool", job)
			return ctx
		}).
		WithTeardown("DeletingUnlabeledProbe", funcs.DeleteResources(FieldManager, manifests, "*.yaml", decoder.MutateNamespace(namespace))).
		Feature()
	testenv.Test(t, f)
}
