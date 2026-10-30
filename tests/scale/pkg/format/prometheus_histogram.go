// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package format renders scale test measurement reports for people.
package format

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvke-telemetry-pipeline/tests/scale/pkg/measurements"
	"github.com/prometheus/common/model"
)

// reportPoint holds one series value or its absence at a query attempt.
type reportPoint struct {
	// kind distinguishes finite values, missing data, errors and non-numeric results.
	kind string
	// value is the original numeric value before display scaling.
	value float64
}

// Format renders query summaries and check findings without modifying results.
// Each label set has independent statistics and numeric histogram counts.
// Histograms count finite query observations, not requests or elapsed time.
// Other report types fall back to indented JSON. Empty reports have a short message.
func Format(report measurements.Report) string {
	if len(report) == 0 {
		return "No measurements."
	}
	queries, ok := report["queries"].(map[string]measurements.QueryReport)
	if !ok {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Sprintf("Cannot format report: %v", err)
		}
		return string(data)
	}
	var out strings.Builder
	if start, ok := report["started_at"].(time.Time); ok {
		fmt.Fprintf(&out, "Started:  %s\n", start.UTC().Format(time.RFC3339))
		if end, ok := report["finished_at"].(time.Time); ok {
			fmt.Fprintf(&out, "Finished: %s\nWindow:   %s\n", end.UTC().Format(time.RFC3339), end.Sub(start).Round(time.Millisecond))
		}
	}
	if interval, ok := report["interval"].(string); ok {
		fmt.Fprintf(&out, "Polling interval: %s\n", interval)
	}
	out.WriteString("Histograms count finite observations.\n")
	for _, name := range slices.Sorted(maps.Keys(queries)) {
		query := queries[name]
		if query.Check != nil {
			formatCheck(&out, name, query)
		} else {
			series := querySeries(query.Observations)
			scale, unit := reportUnit(query.Unit, series)
			out.WriteByte('\n')
			heading := name
			if unit != "" {
				heading += " [" + unit + "]"
			}
			fmt.Fprintln(&out, heading)
			fmt.Fprintf(&out, "  Query: %s\n", query.Expression)
			fmt.Fprintf(&out, "  Attempts: %d\n", len(query.Observations))
			for i, labels := range slices.Sorted(maps.Keys(series)) {
				if i > 0 {
					out.WriteByte('\n')
				}
				formatSeries(&out, labels, series[labels], scale)
			}
		}
		for _, observation := range query.Observations {
			at := observation.EvaluatedAt.UTC().Format(time.RFC3339Nano)
			for _, warning := range observation.Warnings {
				fmt.Fprintf(&out, "  Warning at %s: %q\n", at, warning)
			}
			if observation.Error != "" {
				fmt.Fprintf(&out, "  Error at %s: %q\n", at, observation.Error)
			}
			if value := observation.Value; query.Check == nil && value != nil && value.Type() != model.ValVector && value.Type() != model.ValScalar {
				fmt.Fprintf(&out, "  Result at %s: %s is retained in the raw report and has no numeric summary\n", at, value.Type())
			}
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// formatCheck shows the check outcome and any series that fired.
func formatCheck(out *strings.Builder, name string, query measurements.QueryReport) {
	fmt.Fprintf(out, "\n%s\n  Check: %s\n  For: %s\n  Attempts: %d\n", name, query.Expression, query.Check.For, len(query.Observations))
	if query.Check.Action == measurements.Complete {
		out.WriteString("  Action: complete\n")
	}
	if query.Check.FiredAt == nil {
		out.WriteString("  Check did not fire.\n")
	} else {
		fmt.Fprintf(out, "  FIRED at %s\n", query.Check.FiredAt.UTC().Format(time.RFC3339Nano))
		for _, labels := range query.Check.Series {
			fmt.Fprintf(out, "  Series: %s\n", labels)
		}
	}
}

// querySeries aligns each full label set with every query attempt, retaining gaps.
func querySeries(observations []measurements.QueryObservation) map[string][]reportPoint {
	base := make([]reportPoint, len(observations))
	for i, observation := range observations {
		if observation.Error != "" {
			base[i].kind = "error"
			continue
		}
		switch observation.Value.(type) {
		case nil, *model.Scalar, model.Vector:
		default:
			base[i].kind = "unsupported"
		}
	}
	series := make(map[string][]reportPoint)
	add := func(labels string, index int, point reportPoint) {
		if _, ok := series[labels]; !ok {
			series[labels] = slices.Clone(base)
		}
		series[labels][index] = point
	}
	for i, observation := range observations {
		if observation.Error != "" {
			continue
		}
		switch value := observation.Value.(type) {
		case *model.Scalar:
			add("{}", i, numericPoint(float64(value.Value)))
		case model.Vector:
			for _, sample := range value {
				point := numericPoint(float64(sample.Value))
				if sample.Histogram != nil {
					point.kind = "unsupported"
				}
				add(sample.Metric.String(), i, point)
			}
		}
	}
	if len(series) == 0 {
		series["{}"] = base
	}
	return series
}

// numericPoint separates non-finite values from histogram and statistics inputs.
func numericPoint(value float64) reportPoint {
	kind := "value"
	switch {
	case math.IsNaN(value):
		kind = "NaN"
	case math.IsInf(value, 1):
		kind = "+Inf"
	case math.IsInf(value, -1):
		kind = "-Inf"
	}
	return reportPoint{kind: kind, value: value}
}

// reportUnit selects one byte scale shared by every series in a query.
// Other explicit units are displayed verbatim and never inferred from PromQL.
func reportUnit(unit string, series map[string][]reportPoint) (float64, string) {
	if unit != "bytes" && unit != "bytes/s" {
		return 1, unit
	}
	largest := 0.0
	for _, points := range series {
		for _, point := range points {
			if point.kind == "value" {
				largest = math.Max(largest, math.Abs(point.value))
			}
		}
	}
	scale := 1.0
	name := "B"
	for _, prefix := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		if largest/scale < 1024 {
			break
		}
		scale *= 1024
		name = prefix
	}
	if unit == "bytes/s" {
		name += "/s"
	}
	return scale, name
}

// formatSeries writes finite statistics and keeps missing or invalid values visible.
func formatSeries(out *strings.Builder, labels string, points []reportPoint, scale float64) {
	counts := make(map[string]int)
	var values []float64
	for _, point := range points {
		counts[point.kind]++
		if point.kind == "value" {
			values = append(values, point.value/scale)
		}
	}
	fmt.Fprintf(out, "  Series: %s\n", labels)
	fmt.Fprintf(out, "    Values: %d  No data: %d  Errors: %d\n", len(values), counts[""], counts["error"])
	fmt.Fprintf(out, "    NaN: %d  +Inf: %d  -Inf: %d  Unsupported: %d\n", counts["NaN"], counts["+Inf"], counts["-Inf"], counts["unsupported"])
	last := "n/a"
	if len(points) != 0 {
		point := points[len(points)-1]
		last = point.kind
		switch point.kind {
		case "value":
			last = reportNumber(point.value / scale)
		case "":
			last = "no data"
		}
	}
	if len(values) == 0 {
		fmt.Fprintf(out, "    No finite values. Last: %s\n", last)
		return
	}
	low, high := slices.Min(values), slices.Max(values)
	mean := 0.0
	for _, value := range values {
		mean += value / float64(len(values))
	}
	fmt.Fprintf(out, "    Min: %s  Mean: %s  Max: %s  Last: %s\n", reportNumber(low), reportNumber(mean), reportNumber(high), last)
	formatHistogram(out, values, low, high)
}

// reportNumber uses enough precision for useful summaries of small and large values.
func reportNumber(value float64) string {
	return strconv.FormatFloat(value, 'g', 6, 64)
}

// formatHistogram splits the observed range into five equal-width buckets.
// Boundaries that coincide at float64 precision are merged. The last upper bound
// is inclusive, and constant values occupy a single bucket.
func formatHistogram(out *strings.Builder, values []float64, low, high float64) {
	if low == high {
		formatHistogramRows(out, []string{reportNumber(low)}, []int{len(values)})
		return
	}
	bounds := []float64{low}
	for i := 1; i <= 5; i++ {
		fraction := float64(i) / 5
		// Interpolate endpoints to avoid overflowing high-low.
		bound := min(high, low*(1-fraction)+high*fraction)
		if bound > bounds[len(bounds)-1] {
			bounds = append(bounds, bound)
		}
	}
	counts := make([]int, len(bounds)-1)
	for _, value := range values {
		for i := range counts {
			if value < bounds[i+1] || i == len(counts)-1 {
				counts[i]++
				break
			}
		}
	}
	ranges := make([]string, len(counts))
	for i := range counts {
		closing := ")"
		if i == len(counts)-1 {
			closing = "]"
		}
		left, right := reportNumber(bounds[i]), reportNumber(bounds[i+1])
		if left == right {
			left = strconv.FormatFloat(bounds[i], 'g', -1, 64)
			right = strconv.FormatFloat(bounds[i+1], 'g', -1, 64)
		}
		ranges[i] = fmt.Sprintf("[%s, %s%s", left, right, closing)
	}
	formatHistogramRows(out, ranges, counts)
}

// formatHistogramRows aligns range headings and right-aligned counts using shared widths.
func formatHistogramRows(out *strings.Builder, ranges []string, counts []int) {
	rangeWidth, countWidth := len("Range"), len("Observations")
	for i, label := range ranges {
		rangeWidth = max(rangeWidth, len(label))
		countWidth = max(countWidth, len(strconv.Itoa(counts[i])))
	}
	fmt.Fprintf(out, "    %-*s  %*s\n", rangeWidth, "Range", countWidth, "Observations")
	for i, label := range ranges {
		fmt.Fprintf(out, "    %-*s  %*d\n", rangeWidth, label, countWidth, counts[i])
	}
}
