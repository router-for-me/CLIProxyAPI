package cachestats

import (
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func codexRecord(model string, at time.Time, input, cacheRead, output int64, failed bool) coreusage.Record {
	return coreusage.Record{
		Provider:    "codex",
		Model:       model,
		RequestedAt: at,
		Failed:      failed,
		Detail: coreusage.Detail{
			InputTokens:     input,
			CacheReadTokens: cacheRead,
			OutputTokens:    output,
		},
	}
}

func TestAggregatorHitRates(t *testing.T) {
	agg := &aggregator{models: make(map[string]map[int64]*Bucket)}
	now := time.Date(2026, 9, 19, 12, 30, 0, 0, time.UTC)

	// Codex semantics: cache tokens are a subset of input_tokens.
	agg.observe(codexRecord("gpt-5.6-sol", now, 18000, 0, 20, false), now)
	agg.observe(codexRecord("gpt-5.6-sol", now, 18000, 17000, 20, false), now)
	agg.observe(codexRecord("gpt-6-astra", now, 1000, 0, 10, true), now)

	snap := agg.snapshot(24, true, now)
	if len(snap.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(snap.Models), snap.Models)
	}
	sol := snap.Models[0]
	if sol.Model != "gpt-5.6-sol" {
		sol = snap.Models[1]
	}
	if sol.Requests != 2 || sol.CacheHitRequests != 1 {
		t.Fatalf("sol requests=%d hits=%d, want 2/1", sol.Requests, sol.CacheHitRequests)
	}
	if sol.InputTokens != 36000 || sol.CacheReadTokens != 17000 || sol.UncachedTokens != 19000 {
		t.Fatalf("sol tokens input=%d read=%d uncached=%d", sol.InputTokens, sol.CacheReadTokens, sol.UncachedTokens)
	}
	if sol.RequestHitRate != 0.5 {
		t.Fatalf("sol request hit rate = %v, want 0.5", sol.RequestHitRate)
	}
	want := float64(17000) / float64(36000)
	if sol.TokenHitRate != want {
		t.Fatalf("sol token hit rate = %v, want %v", sol.TokenHitRate, want)
	}
	if len(sol.Hourly) != 1 || sol.Hourly[0].Requests != 2 {
		t.Fatalf("sol hourly = %+v", sol.Hourly)
	}
}

func TestAggregatorWindowAndRetention(t *testing.T) {
	agg := &aggregator{models: make(map[string]map[int64]*Bucket)}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	agg.observe(codexRecord("m", now.Add(-30*time.Hour), 100, 50, 5, false), now)
	agg.observe(codexRecord("m", now, 100, 100, 5, false), now)

	if snap := agg.snapshot(24, false, now); snap.Models[0].Requests != 1 {
		t.Fatalf("24h window requests = %d, want 1", snap.Models[0].Requests)
	}
	if snap := agg.snapshot(48, false, now); snap.Models[0].Requests != 2 {
		t.Fatalf("48h window requests = %d, want 2", snap.Models[0].Requests)
	}

	// Buckets past the retention horizon are dropped on the next observe.
	agg.observe(codexRecord("m", now.Add(-(retentionHours+2)*time.Hour), 1, 0, 0, false), now)
	if snap := agg.snapshot(retentionHours, false, now); snap.Models[0].Requests != 2 {
		t.Fatalf("retention window requests = %d, want 2", snap.Models[0].Requests)
	}
}
