// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package outagereport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVolumeGrowthAndRates(t *testing.T) {
	sample := func(seconds int, phase string, allocated, apparent, writes float64) VolumeSample {
		time := time.Unix(int64(seconds), 0)
		return VolumeSample{Time: time, Phase: phase, AllocatedBytes: &allocated, ApparentBytes: &apparent,
			Devices: map[string]VolumeCounters{"sda": {Time: time, Values: Metrics{"container_fs_writes_bytes_total": writes}}}}
	}
	samples := []VolumeSample{
		sample(0, "baseline", 4096, 1<<20, 10),
		sample(5, "baseline", 8192, 2<<20, 110),
		sample(5, "baseline", 8192, 2<<20, 110), // Cached cAdvisor timestamp.
		sample(10, "baseline", 12288, 3<<20, 310),
		sample(15, "outage", 16384, 4<<20, 900), // Cross-phase interval excluded.
		sample(20, "outage", 8192, 4<<20, 1000),
		sample(25, "outage", 8192, 4<<20, 5), // Counter reset excluded.
		{Time: time.Unix(30, 0), Phase: "outage", IOError: "missing", UsageError: "exec failed"},
		sample(35, "outage", 8192, 4<<20, 2000), // Gap excluded.
	}
	r := SummarizeVolume(samples)
	assert.Equal(t, float64(16384), *r.PeakAllocatedBytes)
	assert.Equal(t, float64(12288), *r.PeakGrowthBytes)
	assert.Equal(t, float64(4<<20), *r.PeakApparentBytes)
	assert.Equal(t, 1, r.IOErrors)
	assert.Equal(t, 1, r.UsageErrors)
	assert.Equal(t, 1, r.CounterResets)
	assert.True(t, r.IOActivityObserved)
	baseline := r.Phases["baseline"]["sda"]
	assert.Equal(t, 2, baseline.Intervals)
	assert.Equal(t, float64(10), baseline.Seconds)
	assert.Equal(t, float64(300), baseline.Totals["container_fs_writes_bytes_total"])
	assert.Equal(t, float64(30), baseline.Average["container_fs_writes_bytes_total"])
	assert.Equal(t, float64(40), baseline.Peak["container_fs_writes_bytes_total"])
	assert.Equal(t, 1, r.Phases["outage"]["sda"].Intervals)
}
