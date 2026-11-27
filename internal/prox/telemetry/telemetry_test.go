package telemetry

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func TestSeriesAggregation(t *testing.T) {
	r := New(168 * time.Hour)
	r.Usage(t0, "claude", "A", false, 200, 100, 50, 0, 900, 10)
	r.Usage(t0.Add(time.Minute), "claude", "A", true, 429, 0, 0, 0, 0, 0)
	r.Usage(t0.Add(7*time.Minute), "claude", "B", true, 401, 0, 0, 0, 0, 0)
	r.Decision(t0, "claude", "A", "failover")
	pts := r.Series("claude", t0, t0.Add(10*time.Minute), 5*time.Minute)
	if len(pts) != 3 || pts[0].Requests != 2 || pts[0].CacheRead != 900 || pts[0].RateLimited != 1 || pts[0].Failovers != 1 || pts[1].AuthFailures != 1 {
		t.Fatalf("series: %+v", pts)
	}
	if tot := r.Totals("claude|A", t0); tot.Requests != 2 || tot.InputTokens != 100 {
		t.Fatalf("totals: %+v", tot)
	}
}

func TestBurnEstimates(t *testing.T) {
	reset := t0.Add(10 * time.Hour)
	r := New(168 * time.Hour)
	if e := r.Burn("A", "weekly", t0, 10, &reset); e.Status != "insufficient_data" {
		t.Fatalf("no samples must be insufficient: %+v", e)
	}
	r.Quota(t0, "A", "weekly", 10, &reset)
	r.Quota(t0.Add(10*time.Minute), "A", "weekly", 12, &reset)
	if e := r.Burn("A", "weekly", t0.Add(10*time.Minute), 12, &reset); e.Status != "insufficient_data" {
		t.Fatalf("short span must be insufficient: %+v", e)
	}
	r.Quota(t0.Add(60*time.Minute), "A", "weekly", 20, &reset)
	e := r.Burn("A", "weekly", t0.Add(60*time.Minute), 20, &reset)
	if e.Status != "ok" || e.RatePerHour < 9 || e.RatePerHour > 11 || e.DepletesAt == nil || !e.BeforeReset {
		t.Fatalf("10%%/h with 80%% left and 9h to reset must deplete before reset: %+v", e)
	}
	// A new window (reset moved) starts a fresh estimate rather than a negative rate.
	next := reset.Add(7 * 24 * time.Hour)
	r.Quota(t0.Add(70*time.Minute), "A", "weekly", 0, &next)
	if e := r.Burn("A", "weekly", t0.Add(70*time.Minute), 0, &next); e.Status != "insufficient_data" {
		t.Fatalf("window reset must not mix samples: %+v", e)
	}
	// Slow use projects unused capacity at reset.
	slow := t0.Add(2 * time.Hour)
	r.Quota(t0, "B", "weekly", 10, &slow)
	r.Quota(t0.Add(time.Hour), "B", "weekly", 11, &slow)
	e = r.Burn("B", "weekly", t0.Add(time.Hour), 11, &slow)
	if e.Status != "ok" || e.BeforeReset || e.UnusedAtReset == nil || *e.UnusedAtReset < 87 {
		t.Fatalf("slow burn: %+v", e)
	}
}

func TestPruneAndPersistence(t *testing.T) {
	r := New(48 * time.Hour)
	r.Usage(t0, "codex", "X", false, 200, 1, 1, 0, 0, 0)
	r.Usage(t0.Add(5*time.Minute), "codex", "X", false, 200, 1, 1, 0, 0, 0)
	r.Prune(t0.Add(30 * time.Hour))
	if pts := r.Series("codex", t0, t0.Add(time.Hour), time.Hour); pts[0].Requests != 2 {
		t.Fatalf("fold into hourly lost data: %+v", pts)
	}
	st := r.Export()
	r2 := New(48 * time.Hour)
	r2.Import(st)
	if tot := r2.Totals("codex", t0); tot.Requests != 2 {
		t.Fatalf("import lost data: %+v", tot)
	}
	r2.Prune(t0.Add(72 * time.Hour))
	if tot := r2.Totals("codex", t0); tot.Requests != 0 {
		t.Fatalf("retention not applied: %+v", tot)
	}
}
