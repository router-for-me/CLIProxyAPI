package cachestats

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func record(authIndex string, at time.Time, uncached, read, write int64) coreusage.Record {
	return coreusage.Record{
		AuthIndex:   authIndex,
		Provider:    "claude",
		RequestedAt: at,
		Detail: coreusage.Detail{
			TokenBreakdown: coreusage.NewIndependentTokenBreakdown(uncached, read, write, 10, 0, uncached+read+write+10),
		},
	}
}

func TestStatsRecentWindowAndTotals(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := New()
	s.now = func() time.Time { return now }

	s.HandleUsage(context.Background(), record("1", now.Add(-2*time.Hour), 5, 100, 50))
	s.HandleUsage(context.Background(), record("1", now.Add(-10*time.Minute), 5, 900, 0))
	s.HandleUsage(context.Background(), record("1", now, 5, 800, 20))
	s.HandleUsage(context.Background(), record("2", now, 1, 0, 400))
	failed := record("2", now, 1, 0, 400)
	failed.Failed = true
	s.HandleUsage(context.Background(), failed)
	s.HandleUsage(context.Background(), record("", now, 1, 1, 1))

	got := s.Snapshot()
	if len(got) != 2 {
		t.Fatalf("credentials = %d, want 2", len(got))
	}
	want := Tokens{Requests: 2, Uncached: 10, CacheRead: 1700, CacheWrite: 20}
	if got["1"].Recent != want {
		t.Fatalf("recent = %+v, want %+v", got["1"].Recent, want)
	}
	if got["1"].Total.Requests != 3 || got["1"].Total.CacheWrite != 70 {
		t.Fatalf("total = %+v", got["1"].Total)
	}
	if got["2"].Recent.CacheWrite != 400 || got["2"].Recent.Requests != 1 {
		t.Fatalf("credential 2 = %+v", got["2"].Recent)
	}
}

func TestStatsLateRecordKeepsNewerMinute(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := New()
	s.now = func() time.Time { return now }

	s.HandleUsage(context.Background(), record("1", now, 1, 300, 0))
	// A stream that started an hour ago lands in the same ring slot.
	s.HandleUsage(context.Background(), record("1", now.Add(-time.Hour), 1, 50, 0))

	got := s.Snapshot()["1"]
	if got.Recent.CacheRead != 300 || got.Recent.Requests != 1 {
		t.Fatalf("recent = %+v, want the newer minute kept", got.Recent)
	}
	if got.Total.CacheRead != 350 || got.Total.Requests != 2 {
		t.Fatalf("total = %+v", got.Total)
	}
}
