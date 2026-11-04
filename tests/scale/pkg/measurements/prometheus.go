// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package measurements

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
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
	// completion names the single completion target, or is empty for observations.
	completion string
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
	var completion string
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
			if completion != "" {
				return nil, errors.New("at most one complete check is allowed")
			}
			completion = measurement.Name
		}
	}
	return &Prometheus{config: cfg, completion: completion}, nil
}

// CompletesStage reports whether successful execution requires a target match.
func (p *Prometheus) CompletesStage() bool { return p.completion != "" }

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
// elapses. Queries within a round run sequentially with one evaluation timestamp.
// Slow rounds may skip ticks. A request still running at the end of the window
// is canceled and omitted. Completed observations are retained on every exit.
// Queries record findings without performance verdicts. Checks act on sustained
// vector membership. Empty check results reset pending series.
// Backend warnings fail checks and remain in the report.
// Failed checks, request errors and caller cancellation retain partial reports.
// With a completion target, Run succeeds only after reaching it before duration.
// The entire polling round must finish within duration to confirm completion.
// Errors in the same polling round take precedence over completion.
// Ordinary expiry does not stop generators.
func (p *Prometheus) Run(ctx context.Context) (report Report, err error) {
	if err := ctx.Err(); err != nil {
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
			checks[measurement.Name] = &checkState{hold: check.For, complete: check.Action == Complete}
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
				err = fmt.Errorf("completion check %q was not confirmed before duration", p.completion)
			}
		}
	}()
	client, err := api.NewClient(api.Config{Address: p.config.Endpoint})
	if err != nil {
		return report, fmt.Errorf("create Prometheus client: %w", err)
	}
	queryAPI := prometheusv1.NewAPI(client)
	names := slices.Sorted(maps.Keys(queries))
	ticker := time.NewTicker(p.config.Interval)
	defer ticker.Stop()
	for {
		complete := false
		at := time.Now().UTC()
		for _, name := range names {
			if runCtx.Err() != nil {
				return report, ctx.Err()
			}
			query := queries[name]
			observation, err := queryPrometheus(runCtx, queryAPI, query.Expression, at, p.config.QueryTimeout)
			if errors.Is(err, context.DeadlineExceeded) && runCtx.Err() != nil && ctx.Err() == nil {
				return report, nil
			}
			check := checks[name]
			if err == nil && check != nil {
				query.Check.Series, err = check.evaluate(observation)
				if len(query.Check.Series) > 0 {
					query.Check.FiredAt = &at
				}
			}
			if err != nil {
				observation.Error = err.Error()
			}
			query.Observations = append(query.Observations, observation)
			queries[name] = query
			if err != nil {
				return report, fmt.Errorf("query %q: %w", name, err)
			}
			if query.Check != nil && query.Check.FiredAt != nil {
				if query.Check.Action == Fail {
					return report, fmt.Errorf("check %q fired for %d series", name, len(query.Check.Series))
				}
				complete = true
			}
		}
		if runCtx.Err() != nil {
			return report, ctx.Err()
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

// queryPrometheus retains the official result with a deadline for the entire query.
func queryPrometheus(ctx context.Context, client prometheusv1.API, expression string, at time.Time, timeout time.Duration) (QueryObservation, error) {
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	value, warnings, _, err := client.Query(queryCtx, expression, at)
	return QueryObservation{EvaluatedAt: at, Value: value, Warnings: warnings}, err
}
