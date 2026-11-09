// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/internal/funcs"
	"github.com/stretchr/testify/require"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const (
	// ownershipScrapeInterval is every fixture monitor's interval.
	ownershipScrapeInterval = 15 * time.Second
	// ownershipWindow starts once every target is ready. It spans several
	// Collector target reloads (10s) and scrapes, so a scraper that picks a
	// target up late is still seen.
	ownershipWindow = 2 * time.Minute
	externalService = "nktp-e2e-ownership-external"
)

// ownershipTarget is one scrape target: its job and instance labels.
type ownershipTarget struct{ job, instance string }

func (t ownershipTarget) String() string { return t.job + " " + t.instance }

// poolName returns the nktp_metrics_pool label for display; the node
// collector sets none.
func poolName(pool string) string {
	if pool == "" {
		return "node"
	}
	return pool
}

func TestNodelessMetrics(t *testing.T) {
	const manifests = "manifests/nodeless-metrics"
	namespace := testenv.EnvConf().Namespace()
	probeJob := fmt.Sprintf("probe/%s/nktp-e2e-nodeless-probe", namespace)
	selector := fmt.Sprintf(`up{job=~"nktp-e2e-ownership-.+|scrapeConfig/%s/nktp-e2e-ownership-.+|%s"}`, namespace, probeJob)
	// started is when this run applied its fixtures, and ready is when every
	// target was first scraped successfully by its pool.
	var started, ready time.Time
	// The pool each target belongs to, and the targets in the nktp-e2e class.
	want := map[ownershipTarget]string{}
	classed := map[ownershipTarget]bool{}
	f := features.NewWithDescription("NodelessMetrics",
		"Each target goes to one pool: node targets to the node pool, nodeless ones to the nodeless pool, and large-target monitors' targets to the large-target pool, in the default scrape class or a named one.").
		WithSetup("ApplyingOwnershipFixtures", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			started = time.Now()
			ctx = funcs.ApplyResources(FieldManager, manifests, "*.yaml", decoder.MutateNamespace(namespace))(ctx, t, c)
			k, err := kubernetes.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err)

			exporter, err := k.CoreV1().Services(namespace).Get(ctx, "nktp-blackbox-exporter", metav1.GetOptions{})
			require.NoError(t, err, "getting the blackbox exporter Service")
			external := exporter.Spec.ClusterIP + ":9115"
			// An address with no node or Pod, as for an external endpoint.
			_, err = k.DiscoveryV1().EndpointSlices(namespace).Create(ctx, &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: externalService,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: externalService,
						discoveryv1.LabelManagedBy:   FieldManager,
					},
				},
				AddressType: discoveryv1.AddressTypeIPv4,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{exporter.Spec.ClusterIP}}},
				Ports:       []discoveryv1.EndpointPort{{Name: ptr.To("http"), Port: ptr.To[int32](9115)}},
			}, metav1.CreateOptions{FieldManager: FieldManager})
			require.NoError(t, err, "creating the external Service's EndpointSlice")

			pods, err := k.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=blackbox-exporter"})
			require.NoError(t, err, "listing the blackbox exporter Pods")
			for _, pod := range pods.Items {
				require.NotEmpty(t, pod.Status.PodIP, "blackbox exporter Pod %s has no IP", pod.Name)
				address := pod.Status.PodIP + ":9115"
				want[ownershipTarget{"nktp-e2e-ownership-mixed", address}] = ""
				want[ownershipTarget{"nktp-e2e-ownership-large", address}] = "large-target"
				target := ownershipTarget{"nktp-e2e-ownership-classed", address}
				want[target] = ""
				classed[target] = true
			}
			require.NotEmpty(t, pods.Items, "no blackbox exporter Pods")
			want[ownershipTarget{"nktp-e2e-ownership-mixed", external}] = "nodeless"
			want[ownershipTarget{"nktp-e2e-ownership-large", external}] = "large-target"
			want[ownershipTarget{fmt.Sprintf("scrapeConfig/%s/nktp-e2e-ownership-static", namespace), "nktp-blackbox-exporter:9115"}] = "nodeless"
			target := ownershipTarget{fmt.Sprintf("scrapeConfig/%s/nktp-e2e-ownership-static-classed", namespace), "nktp-blackbox-exporter:9115"}
			want[target] = "nodeless"
			classed[target] = true
			want[ownershipTarget{probeJob, "http://nktp-blackbox-exporter:9115/-/healthy"}] = "nodeless"
			return ctx
		}).
		AssessWithDescription("TargetsReachTheirPools", "Every target is scraped successfully by its pool, the Probe succeeds, and the nktp-e2e class applies to the targets of the monitors that name it.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			services := proxyServices(t, c, namespace)
			// The best up of each target and pool, and the latest probe_success
			// of each pool, since this run applied its fixtures, so a series
			// from an earlier run cannot match.
			upQuery := func() string {
				return fmt.Sprintf(`max by (job, instance, nktp_metrics_pool, nktp_e2e_scrape_class) (max_over_time(%s[%ds]))`,
					selector, rangeSeconds(started))
			}
			probeQuery := func() string {
				return fmt.Sprintf(`max by (nktp_metrics_pool) (last_over_time(probe_success{job=%q}[%ds]))`,
					probeJob, rangeSeconds(started))
			}
			var missing []string
			var probes []prometheusSample
			var lastErr error
			err := wait.For(func(ctx context.Context) (bool, error) {
				var series []prometheusSample
				series, lastErr = prometheusVector(ctx, services, upQuery())
				if lastErr != nil {
					return false, nil
				}
				missing = missingOwnership(series, want, classed)
				probes, lastErr = prometheusVector(ctx, services, probeQuery())
				if lastErr != nil {
					return false, nil
				}
				probed := slices.ContainsFunc(probes, func(s prometheusSample) bool {
					return s.Metric["nktp_metrics_pool"] == "nodeless" && s.Value == 1
				})
				return len(missing) == 0 && probed, nil
			}, wait.WithContext(waitCtx), wait.WithTimeout(0), wait.WithInterval(5*time.Second), wait.WithImmediate())
			require.NoError(t, err, "targets not yet scraped successfully by their pool: %v; want probe_success == 1 for %s from pool nodeless, got %v; last query error: %v",
				missing, probeJob, probes, lastErr)
			ready = time.Now()
			return ctx
		}).
		AssessWithDescription("NoTargetScrapedTwice", "Over discovery, reloads and scraping, each target is scraped by its own pool only, and once targets are ready, by one scraper in it.", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			select {
			case <-ctx.Done():
				require.NoError(t, ctx.Err())
			case <-time.After(ownershipWindow):
			}
			services := proxyServices(t, c, namespace)
			queryCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			// Pools are checked over the whole run; sample counts only since
			// ready, so discovery time does not raise the limit.
			counts := func(since time.Time) (int, []prometheusSample) {
				seconds := rangeSeconds(since)
				series, err := prometheusVector(queryCtx, services, fmt.Sprintf(
					`sum by (job, instance, nktp_metrics_pool) (count_over_time(%s[%ds]))`, selector, seconds))
				require.NoError(t, err)
				return seconds, series
			}
			_, run := counts(started)
			seconds, observed := counts(ready)
			var problems []string
			for _, s := range run {
				target := ownershipTarget{s.Metric["job"], s.Metric["instance"]}
				pool := s.Metric["nktp_metrics_pool"]
				expected, ok := want[target]
				switch {
				case !ok:
					problems = append(problems, fmt.Sprintf("unexpected target %s from pool %s", target, poolName(pool)))
				case pool != expected:
					problems = append(problems, fmt.Sprintf("%s also scraped by pool %s, want only %s", target, poolName(pool), poolName(expected)))
				}
			}
			// One scraper takes at most one sample per interval. A second one in
			// the same pool, whether or not it adds a label, adds more.
			limit := float64(seconds)/ownershipScrapeInterval.Seconds() + 2
			for _, s := range observed {
				target := ownershipTarget{s.Metric["job"], s.Metric["instance"]}
				if pool := s.Metric["nktp_metrics_pool"]; want[target] == pool && s.Value > limit {
					problems = append(problems, fmt.Sprintf("%s has %.0f samples in %ds from pool %s, more than one scraper's %.0f",
						target, s.Value, seconds, poolName(pool), limit))
				}
			}
			require.Empty(t, problems, "targets scraped outside their pool or more than once")
			return ctx
		}).
		WithTeardown("DeletingOwnershipFixtures", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			k, err := kubernetes.NewForConfig(c.Client().RESTConfig())
			require.NoError(t, err)
			err = k.DiscoveryV1().EndpointSlices(namespace).Delete(ctx, externalService, metav1.DeleteOptions{})
			if !apierrors.IsNotFound(err) {
				require.NoError(t, err, "deleting the external Service's EndpointSlice")
			}
			return funcs.DeleteResources(FieldManager, manifests, "*.yaml", decoder.MutateNamespace(namespace))(ctx, t, c)
		}).
		Feature()
	testenv.Test(t, f)
}

// missingOwnership lists the targets in want without a successful scrape from
// their pool, or without the nktp-e2e class label when classed.
func missingOwnership(series []prometheusSample, want map[ownershipTarget]string, classed map[ownershipTarget]bool) []string {
	var missing []string
	for _, target := range slices.SortedFunc(maps.Keys(want), func(a, b ownershipTarget) int {
		return strings.Compare(a.String(), b.String())
	}) {
		found := slices.ContainsFunc(series, func(s prometheusSample) bool {
			return s.Metric["job"] == target.job && s.Metric["instance"] == target.instance &&
				s.Metric["nktp_metrics_pool"] == want[target] && s.Value == 1 &&
				(s.Metric["nktp_e2e_scrape_class"] == "nktp-e2e") == classed[target]
		})
		if !found {
			missing = append(missing, fmt.Sprintf("%s from pool %s", target, poolName(want[target])))
		}
	}
	return missing
}

func proxyServices(t *testing.T, c *envconf.Config, namespace string) coreclient.ServiceInterface {
	client, err := coreclient.NewForConfig(c.Client().RESTConfig())
	require.NoError(t, err, "creating the Service proxy client")
	return client.Services(namespace)
}

// rangeSeconds is a range selector's length that covers this run since
// started, and no earlier.
func rangeSeconds(started time.Time) int {
	return max(1, int(time.Since(started).Seconds()))
}

// prometheusSample is one series of an instant vector.
type prometheusSample struct {
	Metric map[string]string
	Value  float64
}

func prometheusVector(ctx context.Context, services coreclient.ServiceInterface, expression string) ([]prometheusSample, error) {
	body, err := services.ProxyGet("http", "lgtm", "9090", "/api/v1/query", map[string]string{"query": expression}).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying Prometheus for %q: %w", expression, err)
	}
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding Prometheus response for %q: %w", expression, err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus query %q returned status %q: %s", expression, response.Status, response.Error)
	}
	samples := make([]prometheusSample, 0, len(response.Data.Result))
	for _, result := range response.Data.Result {
		text, _ := result.Value[1].(string)
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing value %q of %v for %q: %w", text, result.Metric, expression, err)
		}
		samples = append(samples, prometheusSample{Metric: result.Metric, Value: value})
	}
	return samples, nil
}
