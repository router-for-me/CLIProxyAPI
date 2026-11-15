package apikeylimits

import (
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
