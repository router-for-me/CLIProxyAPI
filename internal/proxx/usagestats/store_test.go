package usagestats

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func rec(model, key string, at time.Time, cost *float64, failed bool) usage.Record {
	d := usage.EnsureTokenBreakdownForProvider(usage.Detail{InputTokens: 1100, CacheReadTokens: 1000, CachedTokens: 1000, OutputTokens: 500, ReasoningTokens: 200}, "codex", "")
	if cost != nil {
		d.CostDetails = &usage.CostDetails{FinalCost: cost, CostSource: "catalog"}
	}
	r := usage.Record{Model: model, Alias: "auto", Provider: "codex", APIKey: key, RequestedAt: at, Failed: failed, Latency: 1500 * time.Millisecond, Detail: d}
	if failed {
		r.Fail.StatusCode = 429
	}
	return r
}

func fp(v float64) *float64 { return &v }

func TestStoreAggregatesAndGroups(t *testing.T) {
	s := NewStore(3)
	day1 := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)
	day2 := day1.Add(2 * time.Minute)
	s.HandleUsage(nil, rec("gpt-5", "sk-aaaaaaaaaaaa1111", day1, fp(0.5), false))
	s.HandleUsage(nil, rec("gpt-5", "sk-bbbbbbbbbbbb2222", day2, fp(0.25), false))
	s.HandleUsage(nil, rec("mystery", "sk-aaaaaaaaaaaa1111", day2, nil, true))

	all := s.Summary("2026-11-15", "2026-11-16", "model")
	if all.Totals.Requests != 3 || all.Totals.Failures != 1 || all.Totals.UnknownCostRequests != 1 {
		t.Fatalf("totals = %+v", all.Totals)
	}
	if all.Totals.InputTokens != 3300 || all.Totals.CachedTokens != 3000 || all.Totals.OutputTokens != 1500 || all.Totals.ReasoningTokens != 600 {
		t.Fatalf("token totals = %+v", all.Totals)
	}
	if all.Totals.CostUSD != 0.75 || all.Rows[0].Key != "gpt-5" {
		t.Fatalf("rows = %+v", all.Rows)
	}
	byDay := s.Summary("2026-11-15", "2026-11-16", "day")
	if len(byDay.Rows) != 2 || byDay.Rows[0].Key != "2026-11-15" || byDay.Rows[0].Requests != 1 {
		t.Fatalf("by day = %+v", byDay.Rows)
	}
	byKey := s.Summary("2026-11-16", "2026-11-16", "api_key")
	if len(byKey.Rows) != 2 {
		t.Fatalf("by key = %+v", byKey.Rows)
	}
	for _, r := range byKey.Rows {
		if strings.Contains(r.Key, "aaaaaaaaaaaa") || !strings.Contains(r.Key, "#") {
			t.Fatalf("api key not masked: %q", r.Key)
		}
	}

	recent := s.Recent(10)
	if len(recent) != 3 || recent[0].Model != "mystery" || recent[0].CostUSD != nil || recent[0].StatusCode != 429 || recent[0].LatencyMS != 1500 {
		t.Fatalf("recent = %+v", recent)
	}
	// Ring buffer keeps the newest 3.
	s.HandleUsage(nil, rec("newest", "", day2, fp(0), false))
	recent = s.Recent(0)
	if len(recent) != 3 || recent[0].Model != "newest" || recent[2].Model != "gpt-5" || recent[2].CostUSD == nil || *recent[2].CostUSD != 0.25 {
		t.Fatalf("ring = %+v", recent)
	}
}

func TestStoreDisabledResetAndPersist(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s := NewStore(0)
	s.SetClock(func() time.Time { return now })
	s.HandleUsage(nil, rec("gpt-5", "k", time.Time{}, fp(1), false)) // zero RequestedAt → store clock
	if got := s.Summary("2026-11-16", "2026-11-16", "provider"); got.Totals.Requests != 1 || got.Rows[0].Key != "codex" {
		t.Fatalf("clock day = %+v", got)
	}

	path := filepath.Join(t.TempDir(), "usage.json")
	if err := s.Save(path, false); err != nil {
		t.Fatal(err)
	}
	loaded := NewStore(0)
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Summary("2026-11-16", "2026-11-16", "model"); got.Totals.CostUSD != 1 || len(loaded.Recent(0)) != 1 {
		t.Fatalf("loaded = %+v", got)
	}

	s.SetEnabled(false)
	s.HandleUsage(nil, rec("gpt-5", "k", now, fp(1), false))
	if got := s.Summary("2026-11-16", "2026-11-16", "model"); got.Totals.Requests != 1 {
		t.Fatalf("disabled store aggregated: %+v", got.Totals)
	}
	s.Reset()
	if got := s.Summary("0000-01-01", "9999-12-31", "model"); got.Totals.Requests != 0 || len(s.Recent(0)) != 0 {
		t.Fatalf("reset = %+v", got)
	}
}

func TestResolveFile(t *testing.T) {
	cases := map[string]string{"": "/etc/proxx/proxx-usage.json", "u.json": "/etc/proxx/u.json", "/abs/u.json": "/abs/u.json"}
	for file, want := range cases {
		cfg := struct{ File string }{file}
		if got := ResolveFile(configFor(cfg.File), "/etc/proxx/config.yaml"); got != want {
			t.Errorf("ResolveFile(%q) = %q, want %q", file, got, want)
		}
	}
}

func configFor(file string) config.ProxxUsageConfig { return config.ProxxUsageConfig{File: file} }
