package engine

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/telemetry"
)

func TestBuildPoolChart(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	resetA := now.Add(30 * time.Hour)
	oldReset := now.Add(-48 * time.Hour)
	// A burns 1%/h for the last day; B reset two days ago and is idle.
	var samplesA []telemetry.Sample
	for h := 24; h >= 0; h-- {
		samplesA = append(samplesA, telemetry.Sample{At: now.Add(-time.Duration(h) * time.Hour), Used: float64(40 - h), ResetsAt: &resetA})
	}
	samplesB := []telemetry.Sample{
		{At: now.Add(-72 * time.Hour), Used: 90, ResetsAt: &oldReset},
		{At: now.Add(-24 * time.Hour), Used: 0},
	}
	in := []chartInput{
		{id: "a", label: "a", remaining: 60, resetsAt: &resetA, period: 7 * 24 * time.Hour, samples: samplesA},
		{id: "b", label: "b", remaining: 100, period: 7 * 24 * time.Hour, samples: samplesB},
	}
	c := buildPoolChart("claude", "weekly", in, now, spanFor("weekly"))

	if c.Capacity != 200 || c.Current != 160 {
		t.Fatalf("capacity/current = %v/%v", c.Capacity, c.Current)
	}
	if c.BurnLast3h != 1 {
		t.Fatalf("burn 3h = %v, want 1", c.BurnLast3h)
	}
	if got := c.Accounts[0].RatePerHour; got != 1 {
		t.Fatalf("rate = %v, want 1", got)
	}
	// 60% at 1%/h lasts 60h, past the reset in 30h.
	if c.FirstEmpty != nil {
		t.Fatalf("first empty = %+v, want none", c.FirstEmpty)
	}
	// B's old window reset 48h ago, so the recorded pool jumps then.
	var sawRecordedReset bool
	for _, ev := range c.Events {
		if !ev.Projected && ev.Gain >= 89 {
			sawRecordedReset = true
		}
	}
	if !sawRecordedReset {
		t.Fatalf("missing recorded reset event: %+v", c.Events)
	}
	// A's reset returns what it burned: 100 - (60 - 30h*1%/h) = 70.
	if c.NextReset == nil || !c.NextReset.At.Equal(resetA) || c.NextReset.Gain != 70 {
		t.Fatalf("next reset = %+v", c.NextReset)
	}
	if c.PoolEmpty != nil {
		t.Fatalf("pool empty = %v", c.PoolEmpty)
	}
	if last := c.Recorded[len(c.Recorded)-1]; !last.At.Equal(now) || last.Value != 160 {
		t.Fatalf("last recorded = %+v", last)
	}
	if c.DrainPeak != 1 {
		t.Fatalf("drain peak = %v", c.DrainPeak)
	}
}

func TestBuildPoolChartFirstEmpty(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	reset := now.Add(100 * time.Hour)
	samples := []telemetry.Sample{
		{At: now.Add(-24 * time.Hour), Used: 52, ResetsAt: &reset},
		{At: now, Used: 100 - 24, ResetsAt: &reset},
	}
	in := []chartInput{{id: "a", label: "a", remaining: 24, resetsAt: &reset, period: 7 * 24 * time.Hour, samples: samples}}
	c := buildPoolChart("claude", "weekly", in, now, spanFor("weekly"))
	if c.FirstEmpty == nil || !c.FirstEmpty.At.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("first empty = %+v", c.FirstEmpty)
	}
	if c.PoolEmpty == nil || c.PoolEmpty.Sub(now) != 24*time.Hour {
		t.Fatalf("pool empty = %v", c.PoolEmpty)
	}
}
