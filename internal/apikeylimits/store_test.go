package apikeylimits

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func newTestStore(now time.Time) *Store {
	s := newStore()
	s.now = func() time.Time { return now }
	return s
}

func TestWindowBoundaries(t *testing.T) {
	loc := time.FixedZone("TRT", 3*3600)
	at := time.Date(2026, 11, 15, 14, 30, 0, 0, loc) // Sunday
	if got := WindowStart(Day, at); !got.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, loc)) {
		t.Fatalf("day start = %v", got)
	}
	if got := WindowStart(Week, at); !got.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, loc)) {
		t.Fatalf("week start = %v, want Monday", got)
	}
	if got := WindowEnd(Month, at); !got.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("month end = %v", got)
	}
}

func TestCheckTokenAndCostLimits(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	s := newTestStore(now)
	limits := config.APIKeyLimits{TokensPerDay: 100, CostPerMonth: 1}
	s.Add("k", 60, 0.4)
	if _, hit := s.Check("k", limits); hit {
		t.Fatal("limit reached too early")
	}
	s.Add("k", 40, 0.1)
	ex, hit := s.Check("k", limits)
	if !hit || ex.Window != Day || ex.Metric != "tokens" || !ex.ResetAt.Equal(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("exceeded = %+v hit=%v", ex, hit)
	}
	// A new day resets the daily counter but keeps the monthly one.
	s.now = func() time.Time { return now.AddDate(0, 0, 1) }
	if _, hit = s.Check("k", limits); hit {
		t.Fatal("daily limit must reset on the next day")
	}
	s.Add("k", 1, 0.6)
	ex, hit = s.Check("k", limits)
	if !hit || ex.Window != Month || ex.Metric != "cost" {
		t.Fatalf("monthly cost limit not reported: %+v", ex)
	}
	if _, hit = s.Check("other", limits); hit {
		t.Fatal("counters must be per key")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	s := newTestStore(now)
	s.path = path
	s.Add("k", 42, 0.25)
	s.Flush()
	restored := newTestStore(now)
	restored.path = path
	restored.load()
	if u := restored.Usage("k")[Day]; u.Tokens != 42 || u.Cost != 0.25 || u.Requests != 1 {
		t.Fatalf("restored usage = %+v", u)
	}
}

func TestRecordCost(t *testing.T) {
	cfg := &config.Config{}
	cfg.ModelPrices = []config.ModelPrice{{Model: "gpt-5*", Input: 2, Output: 10, CachedInput: 0.5}}
	rec := coreusage.Record{Model: "gpt-5.1", Detail: coreusage.Detail{InputTokens: 1_000_000, CachedTokens: 400_000, OutputTokens: 100_000}}
	// 600k*2 + 400k*0.5 + 100k*10 = 1.2 + 0.2 + 1.0 per 1M → 2.4 USD
	if got := RecordCost(cfg, rec); got < 2.399 || got > 2.401 {
		t.Fatalf("cost = %v, want 2.4", got)
	}
	reported := 0.07
	rec.Detail.CostDetails = &coreusage.CostDetails{Cost: &reported}
	if got := RecordCost(cfg, rec); got != reported {
		t.Fatalf("upstream cost must win: %v", got)
	}
	if got := RecordCost(cfg, coreusage.Record{Model: "claude-x", Detail: coreusage.Detail{InputTokens: 10}}); got != 0 {
		t.Fatalf("unpriced model cost = %v", got)
	}
}

func TestHandleUsageCountsNativeKeysOnly(t *testing.T) {
	s := newTestStore(time.Now())
	s.HandleUsage(nil, coreusage.Record{APIKey: "k", IsNativeKey: true, Detail: coreusage.Detail{TotalTokens: 7}})
	s.HandleUsage(nil, coreusage.Record{APIKey: "k", IsNativeKey: false, Detail: coreusage.Detail{TotalTokens: 100}})
	if u := s.Usage("k")[Day]; u.Tokens != 7 || u.Requests != 1 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestHandleUsageBreaksDownByModel(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	s := newTestStore(now)
	s.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k", IsNativeKey: true, Model: "gpt-5.1",
		Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 20, CachedTokens: 40, ReasoningTokens: 5},
	})
	s.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k", IsNativeKey: true, Alias: "claude-sonnet-4-5", Failed: true,
	})
	s.HandleUsage(context.Background(), coreusage.Record{APIKey: "k", Model: "ignored"}) // not a native key

	for _, w := range []Window{Day, Month, Total} {
		u := s.Usage("k")[w]
		if u.Requests != 2 || u.Failed != 1 || u.Tokens != 120 || len(u.Models) != 2 {
			t.Fatalf("%s usage = %+v", w, u)
		}
		gpt := u.Models["gpt-5.1"]
		if gpt == nil || gpt.Requests != 1 || gpt.InputTokens != 100 || gpt.OutputTokens != 20 ||
			gpt.CachedTokens != 40 || gpt.ReasoningTokens != 5 || gpt.Tokens != 120 {
			t.Fatalf("%s gpt usage = %+v", w, gpt)
		}
		if claude := u.Models["claude-sonnet-4-5"]; claude == nil || claude.Requests != 1 || claude.Failed != 1 {
			t.Fatalf("%s alias fallback usage = %+v", w, claude)
		}
	}

	// Snapshots must not alias the store's maps.
	snap := s.Usage("k")[Day]
	snap.Models["gpt-5.1"].Tokens = 0
	if s.Usage("k")[Day].Models["gpt-5.1"].Tokens != 120 {
		t.Fatal("Usage returned a shared model counter")
	}

	// The total window survives day and month rollovers.
	s.now = func() time.Time { return now.AddDate(0, 2, 0) }
	if u := s.Usage("k"); u[Month].Requests != 0 || u[Total].Requests != 2 || u[Total].Models["gpt-5.1"].Tokens != 120 {
		t.Fatalf("after rollover: month=%+v total=%+v", u[Month], u[Total])
	}
}

func TestPersistenceKeepsModelBreakdown(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	s := newTestStore(now)
	s.path = path
	s.AddUsage("k", Usage{Model: "gpt-5.1", InputTokens: 7, OutputTokens: 3, Tokens: 10, Cost: 0.5})
	s.Flush()
	restored := newTestStore(now)
	restored.path = path
	restored.load()
	m := restored.Usage("k")[Total].Models["gpt-5.1"]
	if m == nil || m.Requests != 1 || m.Tokens != 10 || m.InputTokens != 7 || m.Cost != 0.5 {
		t.Fatalf("restored model usage = %+v", m)
	}
}
