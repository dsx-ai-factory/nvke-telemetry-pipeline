// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"go.yaml.in/yaml/v3"
)

// Prometheus collects named PromQL observations and evaluates checks.
// Construct it with NewPrometheus. Each Run owns its report and collection window.
type Prometheus struct {
	// config holds validated collection and connection settings.
	config prometheusConfig
	// completions names the targets that must all hold in the current round.
	completions []string
}

// prometheusConfig defines a collection window independent of generator lifetime.
type prometheusConfig struct {
	Endpoint     string        `yaml:"endpoint"`
	Duration     time.Duration `yaml:"duration"`
	Interval     time.Duration `yaml:"interval"`
	QueryTimeout time.Duration `yaml:"query_timeout"`
	Measurements []Measurement `yaml:"measurements"`
}

// QueryReport retains the expression, ordered polling attempts and check findings.
type QueryReport struct {
	Expression   string             `json:"expression"`
	Unit         string             `json:"unit,omitempty"`
	Observations []QueryObservation `json:"observations"`
	Check        *CheckReport       `json:"check,omitempty"`
}

// QueryObservation retains one official Prometheus result, including empty data.
// Value keeps labels, timestamps and non-finite values in the library's format.
// Backend warnings may indicate incomplete results.
type QueryObservation struct {
	EvaluatedAt time.Time   `json:"evaluated_at"`
	Value       model.Value `json:"value"`
	Warnings    []string    `json:"warnings,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// NewPrometheus validates configuration without contacting the backend.
// Endpoint is an HTTP or HTTPS base URL reachable from the test process.
// Duration and a nonempty measurements sequence are required. Interval defaults to
// 15 seconds and query_timeout to 10 seconds. Durations must be positive.
// Each measurement declares a query or a check. A check hold must be shorter than
// the collection duration. PromQL syntax is validated by the backend.
func NewPrometheus(spec yaml.Node) (*Prometheus, error) {
	if err := validatePrometheusSpec(spec); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(&spec)
	if err != nil {
		return nil, fmt.Errorf("encode prometheus spec: %w", err)
	}
	cfg := prometheusConfig{Interval: 15 * time.Second, QueryTimeout: 10 * time.Second}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode prometheus spec: %w", err)
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("endpoint must be an absolute HTTP or HTTPS base URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, errors.New("endpoint must not contain credentials, query parameters or a fragment")
	}
	if cfg.Duration <= 0 || cfg.Interval <= 0 || cfg.QueryTimeout <= 0 {
		return nil, errors.New("duration, interval and query_timeout must be positive")
	}
	if len(cfg.Measurements) == 0 {
		return nil, errors.New("measurements must contain at least one named PromQL expression")
	}
	var completions []string
	for _, measurement := range cfg.Measurements {
		check := measurement.Check
		if check == nil {
			continue
		}
		if check.For >= cfg.Duration {
			return nil, fmt.Errorf("measurement %q: check for must be shorter than duration", measurement.Name)
		}
		if check.Action == "" {
			check.Action = Fail
		}
		if check.Action == Complete {
			completions = append(completions, measurement.Name)
		}
	}
	return &Prometheus{config: cfg, completions: completions}, nil
}

// CompletesStage reports whether successful execution requires completion checks.
func (p *Prometheus) CompletesStage() bool { return len(p.completions) > 0 }

// validatePrometheusSpec rejects aliases and scalar coercion before typed decoding.
// Typed decoding additionally rejects duplicate options and unknown configuration.
func validatePrometheusSpec(spec yaml.Node) error {
	if spec.Kind != yaml.MappingNode {
		return errors.New("prometheus spec must be a mapping")
	}
	for i := 0; i < len(spec.Content); i += 2 {
		key, value := spec.Content[i], spec.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return errors.New("prometheus option names must be strings")
		}
		if key.Value == "measurements" {
			if err := validateMeasurements(*value); err != nil {
				return err
			}
		} else if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", key.Value)
		}
	}
	return nil
}

// Run queries immediately, then polls at the configured interval until duration
// elapses. Queries within a round run concurrently with one evaluation timestamp.
// Slow rounds may skip ticks. A request still running at the end of the window
// is canceled and omitted. Completed observations are retained on every exit.
// Queries record findings without performance verdicts. Checks act on sustained
// vector membership. Empty check results reset pending series.
// Backend warnings fail checks and remain in the report.
// Failed checks, request errors and caller cancellation retain partial reports.
// Run succeeds only when all completion checks satisfy their holds in one round.
// Completion holds reset after gaps longer than twice the polling interval.
// The entire polling round must finish within that bound and within duration
// to confirm completion.
// Errors in the same polling round take precedence over completion.
// Ordinary expiry does not stop generators.
func (p *Prometheus) Run(ctx context.Context) (Report, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	client, err := api.NewClient(api.Config{Address: p.config.Endpoint})
	if err != nil {
		return nil, fmt.Errorf("create Prometheus client: %w", err)
	}
	return p.run(ctx, prometheusv1.NewAPI(client))
}

// run polls the supplied client and owns all reports, holds and request deadlines.
func (p *Prometheus) run(ctx context.Context, client queryClient) (report Report, err error) {
	err = ctx.Err()
	if err != nil {
		return report, err
	}
	started := time.Now().UTC()
	runCtx, cancel := context.WithTimeout(ctx, p.config.Duration)
	defer cancel()
	queries := make(map[string]QueryReport, len(p.config.Measurements))
	checks := make(map[string]*checkState)
	for _, measurement := range p.config.Measurements {
		query := QueryReport{Expression: measurement.Query, Unit: measurement.Units, Observations: []QueryObservation{}}
		if check := measurement.Check; check != nil {
			query.Expression = check.Expr
			query.Check = &CheckReport{Action: check.Action, For: check.For}
			checks[measurement.Name] = &checkState{hold: check.For, complete: check.Action == Complete, maxGap: 2 * p.config.Interval}
		}
		queries[measurement.Name] = query
	}
	report = Report{"started_at": started, "interval": p.config.Interval.String(), "queries": queries}
	completed := false
	defer func() {
		report["finished_at"] = time.Now().UTC()
		if err == nil {
			err = ctx.Err()
			if err == nil && p.CompletesStage() && !completed {
				err = fmt.Errorf("completion checks %q were not confirmed before duration", p.completions)
			}
		}
	}()
	ticker := time.NewTicker(p.config.Interval)
	defer ticker.Stop()
	for {
		at := time.Now().UTC()
		roundCtx, cancelRound := context.WithCancel(runCtx)
		results := make(chan queryResult, len(queries))
		for name, query := range queries {
			go func() {
				observation, queryErr := queryPrometheus(roundCtx, client, query.Expression, at, p.config.QueryTimeout)
				results <- queryResult{name: name, observation: observation, err: queryErr}
			}()
		}
		var roundErr error
		for range queries {
			result := <-results
			name := result.name
			observation, queryErr := result.observation, result.err
			if queryErr != nil && errors.Is(queryErr, roundCtx.Err()) {
				continue
			}
			query := queries[name]
			check := checks[name]
			if check != nil {
				query.Check.FiredAt = nil
				query.Check.Series = nil
			}
			if queryErr == nil && check != nil {
				query.Check.Series, queryErr = check.evaluate(observation)
				if len(query.Check.Series) > 0 {
					query.Check.FiredAt = &at
				}
			}
			if queryErr != nil {
				observation.Error = queryErr.Error()
				roundErr = errors.Join(roundErr, fmt.Errorf("query %q: %w", name, queryErr))
			} else if query.Check != nil && query.Check.FiredAt != nil && query.Check.Action == Fail {
				roundErr = errors.Join(roundErr, fmt.Errorf("check %q fired for %d series", name, len(query.Check.Series)))
			}
			query.Observations = append(query.Observations, observation)
			queries[name] = query
			if roundErr != nil {
				cancelRound()
			}
		}
		cancelRound()
		if roundErr != nil {
			return report, roundErr
		}
		if runCtx.Err() != nil {
			return report, ctx.Err()
		}
		complete := p.CompletesStage()
		for _, name := range p.completions {
			if time.Since(at) > checks[name].maxGap {
				checks[name].pending = nil
				queries[name].Check.FiredAt = nil
				queries[name].Check.Series = nil
			}
			complete = complete && queries[name].Check.FiredAt != nil
		}
		if complete {
			completed = true
			return report, nil
		}
		select {
		case <-runCtx.Done():
			return report, ctx.Err()
		case <-ticker.C:
		}
	}
}

// queryResult transfers one request result to the goroutine that owns the report.
type queryResult struct {
	name        string
	observation QueryObservation
	err         error
}

// queryClient is the instant query operation used by the runner.
type queryClient interface {
	Query(context.Context, string, time.Time, ...prometheusv1.Option) (model.Value, prometheusv1.Warnings, prometheusv1.Infos, error)
}

// queryPrometheus retains the official result with a deadline for the entire query.
func queryPrometheus(ctx context.Context, client queryClient, expression string, at time.Time, timeout time.Duration) (QueryObservation, error) {
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	value, warnings, _, err := client.Query(queryCtx, expression, at)
	return QueryObservation{EvaluatedAt: at, Value: value, Warnings: warnings}, err
}
