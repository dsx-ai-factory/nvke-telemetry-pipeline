// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package outagereport

import "time"

// VolumeSample keeps directory allocation and container I/O observations separate.
// Missing data has an error and nil values, rather than a measured zero.
type VolumeSample struct {
	Time           time.Time                 `json:"time"`
	Phase          string                    `json:"phase"`
	AllocatedBytes *float64                  `json:"allocated_bytes,omitempty"`
	ApparentBytes  *float64                  `json:"apparent_bytes,omitempty"`
	UsageError     string                    `json:"usage_error,omitempty"`
	IOError        string                    `json:"io_error,omitempty"`
	Devices        map[string]VolumeCounters `json:"devices,omitempty"`
}

// VolumeCounters contains cAdvisor counters for one container/device series.
// Time uses the cAdvisor observation timestamp when one is exposed.
type VolumeCounters struct {
	Time   time.Time `json:"time"`
	Values Metrics   `json:"counters"`
}

// VolumeIO summarizes valid consecutive samples within a single phase and device.
// Totals and averages cover only the reported sampled interval seconds.
type VolumeIO struct {
	Seconds   float64 `json:"sampled_seconds"`
	Intervals int     `json:"intervals"`
	Totals    Metrics `json:"totals"`
	Average   Metrics `json:"average_per_second"`
	Peak      Metrics `json:"peak_per_second"`
}

// VolumeReport contains queue-directory growth and attributable gateway I/O.
// Device totals are deliberately not combined across stacked block devices.
type VolumeReport struct {
	Pod                   string                         `json:"pod"`
	Node                  string                         `json:"node"`
	PVC                   string                         `json:"pvc"`
	PV                    string                         `json:"pv"`
	StorageClass          string                         `json:"storage_class"`
	RequestedBytes        float64                        `json:"requested_bytes"`
	InitialAllocatedBytes *float64                       `json:"initial_allocated_bytes,omitempty"`
	PeakAllocatedBytes    *float64                       `json:"peak_allocated_bytes,omitempty"`
	PeakApparentBytes     *float64                       `json:"peak_apparent_bytes,omitempty"`
	PeakGrowthBytes       *float64                       `json:"peak_growth_bytes,omitempty"`
	UsageErrors           int                            `json:"usage_errors"`
	IOErrors              int                            `json:"io_errors"`
	CounterResets         int                            `json:"counter_resets"`
	IOActivityObserved    bool                           `json:"io_activity_observed"`
	Phases                map[string]map[string]VolumeIO `json:"phases"`
	Samples               []VolumeSample                 `json:"samples"`
	Settling              map[string]VolumeSettlement    `json:"settling,omitempty"`
}

// VolumeSettlement records whether input and observed I/O became quiet before
// the timeout. A quiet interval is not a guarantee that data is durable.
type VolumeSettlement struct {
	Settled      bool    `json:"settled"`
	Reason       string  `json:"reason,omitempty"`
	Seconds      float64 `json:"seconds"`
	QuietSeconds float64 `json:"quiet_seconds"`
}

// SummarizeVolume calculates sampled peaks, growth from the first directory
// observation, and per-device I/O rates. Counter resets and gaps are excluded.
func SummarizeVolume(samples []VolumeSample) *VolumeReport {
	r := &VolumeReport{Samples: samples, Phases: map[string]map[string]VolumeIO{}}
	var phase string
	var phaseStart time.Time
	for i, s := range samples {
		if s.Phase != phase {
			phase, phaseStart = s.Phase, s.Time
		}
		if s.UsageError != "" {
			r.UsageErrors++
		}
		if s.IOError != "" {
			r.IOErrors++
		}
		if s.AllocatedBytes != nil {
			if r.InitialAllocatedBytes == nil {
				r.InitialAllocatedBytes = s.AllocatedBytes
			}
			if r.PeakAllocatedBytes == nil || *s.AllocatedBytes > *r.PeakAllocatedBytes {
				r.PeakAllocatedBytes = s.AllocatedBytes
			}
		}
		if s.ApparentBytes != nil && (r.PeakApparentBytes == nil || *s.ApparentBytes > *r.PeakApparentBytes) {
			r.PeakApparentBytes = s.ApparentBytes
		}
		if i == 0 || s.IOError != "" || samples[i-1].IOError != "" {
			continue
		}
		previous := samples[i-1]
		for device, current := range s.Devices {
			old, ok := previous.Devices[device]
			if !ok {
				continue
			}
			seconds := current.Time.Sub(old.Time).Seconds()
			if seconds <= 0 {
				continue // cAdvisor can return its cached observation repeatedly.
			}
			delta := Metrics{}
			reset := false
			for name, value := range current.Values {
				before, exists := old.Values[name]
				if !exists || value < before {
					reset = true
					break
				}
				delta[name] = value - before
			}
			if reset {
				r.CounterResets++
				continue
			}
			for _, value := range delta {
				if value > 0 {
					r.IOActivityObserved = true
				}
			}
			if s.Phase != previous.Phase || old.Time.Before(phaseStart) {
				continue // Do not assign an interval spanning two phases to either.
			}
			if r.Phases[s.Phase] == nil {
				r.Phases[s.Phase] = map[string]VolumeIO{}
			}
			io := r.Phases[s.Phase][device]
			if io.Totals == nil {
				io.Totals, io.Peak, io.Average = Metrics{}, Metrics{}, Metrics{}
			}
			io.Seconds += seconds
			io.Intervals++
			for name, value := range delta {
				io.Totals[name] += value
				io.Peak[name] = max(io.Peak[name], value/seconds)
				io.Average[name] = io.Totals[name] / io.Seconds
			}
			r.Phases[s.Phase][device] = io
		}
	}
	if r.PeakAllocatedBytes != nil {
		growth := *r.PeakAllocatedBytes - *r.InitialAllocatedBytes
		r.PeakGrowthBytes = &growth
	}
	return r
}
