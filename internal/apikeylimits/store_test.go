package apikeylimits

import (
	"context"
	"os"
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

func TestHistoryBucketsAndRetention(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	s := newTestStore(now)
	s.path = path
	s.AddUsage("k", Usage{Model: "m1", Tokens: 10})
	s.now = func() time.Time { return now.Add(time.Hour) }
	s.AddUsage("k", Usage{Model: "m1", Tokens: 5, Failed: true})
	s.AddUsage("k", Usage{Tokens: 1}) // no model → "unknown" in history only

	daily, hourly := s.History("k")
	if len(daily) != 1 || daily[0].Time != "2026-11-15" || daily[0].Models["m1"].Tokens != 15 ||
		daily[0].Models["m1"].Failed != 1 || daily[0].Models["unknown"].Tokens != 1 {
		t.Fatalf("daily = %+v", daily)
	}
	if len(hourly) != 2 || hourly[0].Time != "2026-11-15T10" || hourly[1].Time != "2026-11-15T11" ||
		hourly[0].Models["m1"].Tokens != 10 {
		t.Fatalf("hourly = %+v", hourly)
	}

	s.Flush()
	restored := newTestStore(now.Add(time.Hour))
	restored.path = path
	restored.load()
	if d, h := restored.History("k"); len(d) != 1 || len(h) != 2 || d[0].Models["m1"].Tokens != 15 {
		t.Fatalf("restored history daily=%+v hourly=%+v", d, h)
	}

	// Hourly buckets expire after HistoryHours, daily ones after HistoryDays.
	s.now = func() time.Time { return now.Add(HistoryHours * time.Hour) }
	if d, h := s.History("k"); len(d) != 1 || len(h) != 1 || h[0].Time != "2026-11-15T11" {
		t.Fatalf("after 48h daily=%+v hourly=%+v", d, h)
	}
	s.now = func() time.Time { return now.AddDate(0, 0, HistoryDays) }
	if d, h := s.History("k"); d != nil || h != nil {
		t.Fatalf("after retention daily=%+v hourly=%+v", d, h)
	}
}

func TestAddRejectedCountsSeparately(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	s := newTestStore(now)
	s.path = path
	s.AddRejected("k", "api_key_disabled")
	s.AddRejected("k", "api_key_disabled")
	s.AddRejected("k", "api_key_expired")
	u := s.Usage("k")[Day]
	if u.Rejected != 3 || u.Requests != 0 || u.RejectedReasons["api_key_disabled"] != 2 ||
		u.LastRejectedAt == nil || !u.LastRejectedAt.Equal(now) {
		t.Fatalf("rejections = %+v", u)
	}
	u.RejectedReasons["api_key_disabled"] = 99
	if s.Usage("k")[Day].RejectedReasons["api_key_disabled"] != 2 {
		t.Fatal("Usage returned a shared reasons map")
	}
	s.Flush()
	restored := newTestStore(now)
	restored.path = path
	restored.load()
	if r := restored.Usage("k")[Total]; r.Rejected != 3 || r.RejectedReasons["api_key_expired"] != 1 {
		t.Fatalf("restored rejections = %+v", r)
	}
}

func TestNormalizeTokensAcrossConventions(t *testing.T) {
	// Anthropic: input_tokens excludes cache reads/writes; the executor builds an
	// independent breakdown.
	claude := coreusage.Detail{
		InputTokens: 100, CacheReadTokens: 900, CachedTokens: 900, CacheCreationTokens: 50, OutputTokens: 20,
		TokenBreakdown: coreusage.NewIndependentTokenBreakdown(100, 900, 50, 20, 0, 1070),
	}
	if got := normalizeTokens(claude); got != (tokenCounts{input: 1050, cacheRead: 900, cacheWrite: 50, output: 20}) {
		t.Fatalf("claude = %+v", got)
	}
	// OpenAI/Codex: input_tokens already includes cached tokens.
	codex := coreusage.Detail{
		InputTokens: 1000, CachedTokens: 900, CacheReadTokens: 900, OutputTokens: 20,
		TokenBreakdown: coreusage.NewSubsetTokenBreakdown(1000, 900, 0, 20, 0, 1020),
	}
	if got := normalizeTokens(codex); got != (tokenCounts{input: 1000, cacheRead: 900, output: 20}) {
		t.Fatalf("codex = %+v", got)
	}
	// Without a breakdown, cache fields larger than input mean they were reported separately.
	raw := coreusage.Detail{InputTokens: 10, CacheReadTokens: 500, CacheCreationTokens: 5, OutputTokens: 3}
	if got := normalizeTokens(raw); got != (tokenCounts{input: 515, cacheRead: 500, cacheWrite: 5, output: 3}) {
		t.Fatalf("raw anthropic = %+v", got)
	}
	rawOpenAI := coreusage.Detail{InputTokens: 100, CachedTokens: 40, OutputTokens: 3}
	if got := normalizeTokens(rawOpenAI); got != (tokenCounts{input: 100, cacheRead: 40, output: 3}) {
		t.Fatalf("raw openai = %+v", got)
	}
}

func TestHandleUsageClaudeCountsCacheInInput(t *testing.T) {
	s := newTestStore(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	s.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k", IsNativeKey: true, Model: "claude-opus-5-5",
		Detail: coreusage.Detail{
			InputTokens: 100, CacheReadTokens: 900, CachedTokens: 900, CacheCreationTokens: 50, OutputTokens: 20,
			TotalTokens:    1070,
			TokenBreakdown: coreusage.NewIndependentTokenBreakdown(100, 900, 50, 20, 0, 1070),
		},
	})
	m := s.Usage("k")[Day].Models["claude-opus-5-5"]
	if m.InputTokens != 1050 || m.CachedTokens != 900 || m.CacheWriteTokens != 50 || m.OutputTokens != 20 || m.Tokens != 1070 {
		t.Fatalf("claude model usage = %+v", m)
	}
}

func TestRecordCostClaudeUsesCacheReads(t *testing.T) {
	cfg := &config.Config{}
	cfg.ModelPrices = []config.ModelPrice{{Model: "claude-*", Input: 10, Output: 50, CachedInput: 1}}
	rec := coreusage.Record{Model: "claude-opus-5-5", Detail: coreusage.Detail{
		InputTokens: 100_000, CacheReadTokens: 900_000, CachedTokens: 900_000, OutputTokens: 10_000,
		TokenBreakdown: coreusage.NewIndependentTokenBreakdown(100_000, 900_000, 0, 10_000, 0, 1_010_000),
	}}
	// 100k*10 + 900k*1 + 10k*50 per 1M = 1.0 + 0.9 + 0.5 = 2.4 USD
	if got := RecordCost(cfg, rec); got < 2.399 || got > 2.401 {
		t.Fatalf("cost = %v, want 2.4", got)
	}
}

func TestLoadMigratesV1AnthropicInput(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	fp := Fingerprint("k")
	v1 := `{"version":1,"counters":{"` + fp + `":{"total":{"start":"0001-01-01T00:00:00Z","tokens":0,"cost":0,"requests":3,` +
		`"models":{"claude-opus-5-5":{"requests":1,"input_tokens":117,"output_tokens":5,"cached_tokens":1800000,"tokens":1800122,"cost":0},` +
		`"gpt-6.1-sol":{"requests":1,"input_tokens":1000,"output_tokens":5,"cached_tokens":900,"tokens":1005,"cost":0},` +
		`"other-proxy":{"requests":1,"input_tokens":10,"output_tokens":5,"cached_tokens":40,"tokens":55,"cost":0}}}}},` +
		`"history":{"` + fp + `":{"daily":{"2026-11-17":{"claude-opus-5-5":{"requests":1,"input_tokens":117,"output_tokens":5,"cached_tokens":1800000,"tokens":1800122,"cost":0}}}}}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestStore(now)
	s.path = path
	s.load()
	models := s.Usage("k")[Total].Models
	if models["claude-opus-5-5"].InputTokens != 1_800_117 || models["gpt-6.1-sol"].InputTokens != 1000 ||
		models["other-proxy"].InputTokens != 50 {
		t.Fatalf("migrated models = claude %+v gpt %+v other %+v", models["claude-opus-5-5"], models["gpt-6.1-sol"], models["other-proxy"])
	}
	if daily, _ := s.History("k"); len(daily) != 1 || daily[0].Models["claude-opus-5-5"].InputTokens != 1_800_117 {
		t.Fatalf("migrated history = %+v", daily)
	}
	// Re-saving writes version 2, which must not be migrated again.
	s.dirty = true
	s.Flush()
	again := newTestStore(now)
	again.path = path
	again.load()
	if got := again.Usage("k")[Total].Models["claude-opus-5-5"].InputTokens; got != 1_800_117 {
		t.Fatalf("v2 reload input = %d", got)
	}
}

func TestHandleUsageTracksEffortTierAndLatency(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	s := newTestStore(now)
	s.path = path
	rec := func(effort, tier string, latency time.Duration, failed bool) coreusage.Record {
		return coreusage.Record{
			APIKey: "k", IsNativeKey: true, Model: "gpt-6.1-sol", ReasoningEffort: effort,
			ResponseServiceTier: tier, Stream: true, Latency: latency, TTFT: latency / 4, Failed: failed,
			Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 8, TotalTokens: 120},
		}
	}
	s.HandleUsage(context.Background(), rec(" High ", "priority", 4*time.Second, false))
	s.HandleUsage(context.Background(), rec("high", "priority", 2*time.Second, true))
	s.HandleUsage(context.Background(), rec("", "", 0, false))

	m := s.Usage("k")[Day].Models["gpt-6.1-sol"]
	if m.Streamed != 3 || m.LatencyMs != 6000 || m.LatencyCount != 2 || m.TTFTMs != 1500 || m.TTFTCount != 2 {
		t.Fatalf("model timing = %+v", m)
	}
	high := m.Efforts["high"]
	if high == nil || high.Requests != 2 || high.Failed != 1 || high.Tokens != 240 || high.ReasoningTokens != 16 ||
		high.LatencyMs != 6000 || m.Efforts[DefaultDimValue] == nil || m.Efforts[DefaultDimValue].Requests != 1 {
		t.Fatalf("efforts = %+v", m.Efforts)
	}
	if m.Tiers["priority"] == nil || m.Tiers["priority"].Requests != 2 || m.Tiers[DefaultDimValue].Requests != 1 {
		t.Fatalf("tiers = %+v", m.Tiers)
	}

	// Snapshots and persistence keep the breakdown without sharing maps.
	m.Efforts["high"].Requests = 99
	if s.Usage("k")[Day].Models["gpt-6.1-sol"].Efforts["high"].Requests != 2 {
		t.Fatal("Usage returned a shared effort counter")
	}
	s.Flush()
	restored := newTestStore(now)
	restored.path = path
	restored.load()
	daily, _ := restored.History("k")
	if len(daily) != 1 || daily[0].Models["gpt-6.1-sol"].Efforts["high"].Requests != 2 {
		t.Fatalf("restored history = %+v", daily)
	}
}

func TestDimValuesAreCapped(t *testing.T) {
	s := newTestStore(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	for i := 0; i < maxDimValues+5; i++ {
		s.AddUsage("k", Usage{Model: "m", Effort: "e" + string(rune('a'+i))})
	}
	efforts := s.Usage("k")[Day].Models["m"].Efforts
	if len(efforts) != maxDimValues+1 || efforts["other"] == nil || efforts["other"].Requests != 5 {
		t.Fatalf("efforts = %d entries, other=%+v", len(efforts), efforts["other"])
	}
}

func TestAccountsAndRecentRequests(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	s := newTestStore(now)
	s.path = path
	s.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k", IsNativeKey: true, Model: "m1", AuthID: "acc-1", Provider: "codex", TraceID: "t1",
		Stream: true, Latency: 3 * time.Second, TTFT: time.Second,
		Detail: coreusage.Detail{InputTokens: 100, CachedTokens: 40, OutputTokens: 20, TotalTokens: 120},
	})
	s.now = func() time.Time { return now.Add(time.Minute) }
	s.HandleUsage(context.Background(), coreusage.Record{
		APIKey: "k", IsNativeKey: true, Model: "m1", AuthID: "acc-2", Failed: true,
		Fail: coreusage.Failure{StatusCode: 429},
	})

	accounts := s.Usage("k")[Day].Accounts
	if len(accounts) != 2 || accounts["acc-1"].Requests != 1 || accounts["acc-1"].Tokens != 120 || accounts["acc-2"].Failed != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
	recent := s.Recent(0)
	if len(recent) != 2 || recent[0].AuthID != "acc-2" || recent[0].StatusCode != 429 || !recent[0].Failed ||
		recent[1].RequestID != "t1" || recent[1].LatencyMs != 3000 || recent[1].TTFTMs != 1000 ||
		recent[1].CachedTokens != 40 || recent[1].KeyFP != Fingerprint("k") || recent[1].Effort != DefaultDimValue {
		t.Fatalf("recent = %+v", recent)
	}
	if got := s.Recent(1); len(got) != 1 || got[0].AuthID != "acc-2" {
		t.Fatalf("limited recent = %+v", got)
	}

	for i := 0; i < RecentLimit+10; i++ {
		s.AddUsage("k", Usage{Model: "m2"})
	}
	if got := s.Recent(0); len(got) != RecentLimit || got[len(got)-1].Model != "m2" {
		t.Fatalf("ring size = %d", len(got))
	}
	s.Flush()
	restored := newTestStore(now)
	restored.path = path
	restored.load()
	if len(restored.Recent(0)) != RecentLimit || restored.Usage("k")[Day].Accounts["acc-1"].Requests != 1 {
		t.Fatal("recent requests or accounts not restored")
	}
}

func TestPriceTokensUsesCacheWritePrice(t *testing.T) {
	price := config.ModelPrice{Input: 4, Output: 20, CachedInput: 0.2, CacheWrite: 5}
	// fresh 100k*4 + read 800k*0.2 + write 100k*5 + out 10k*20 = 0.4+0.16+0.5+0.2 = 1.26 USD
	got := priceTokens(price, tokenCounts{input: 1_000_000, cacheRead: 800_000, cacheWrite: 100_000, output: 10_000})
	if got < 1.2599 || got > 1.2601 {
		t.Fatalf("cost = %v, want 1.26", got)
	}
	// Without cache prices both fall back to the input price.
	if got = priceTokens(config.ModelPrice{Input: 1, Output: 2}, tokenCounts{input: 1_000_000, cacheRead: 500_000, output: 0}); got < 0.9999 || got > 1.0001 {
		t.Fatalf("fallback cost = %v", got)
	}
}

func TestBackfillCostsPricesStoredUsageOnce(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "api-key-usage.json")
	cfg := &config.Config{}
	cfg.ModelPrices = []config.ModelPrice{{Model: "priced-*", Input: 2, Output: 10, CachedInput: 0.5}}
	s := newTestStore(now)
	s.path = path
	s.cfgFn = func() *config.Config { return cfg }
	s.AddUsage("k", Usage{Model: "priced-a", InputTokens: 1_000_000, CachedTokens: 400_000, OutputTokens: 100_000,
		Tokens: 1_100_000, Effort: "high", AuthID: "acc"})
	s.AddUsage("k", Usage{Model: "unpriced", InputTokens: 10, OutputTokens: 10, Tokens: 20})
	s.AddUsage("k", Usage{Model: "priced-b", Tokens: 50, Cost: 7}) // real cost is kept

	s.BackfillCosts()
	// 600k*2 + 400k*0.5 + 100k*10 = 1.2 + 0.2 + 1.0 = 2.4 USD
	day := s.Usage("k")[Day]
	m := day.Models["priced-a"]
	if m.Cost < 2.399 || m.Cost > 2.401 || day.Models["unpriced"].Cost != 0 || day.Models["priced-b"].Cost != 7 {
		t.Fatalf("models = a %+v unpriced %+v b %+v", m, day.Models["unpriced"], day.Models["priced-b"])
	}
	if day.Cost < 9.399 || day.Cost > 9.401 || m.Efforts["high"].Cost < 2.399 || day.Accounts["acc"].Cost <= 0 {
		t.Fatalf("window cost = %v effort = %+v account = %+v", day.Cost, m.Efforts["high"], day.Accounts["acc"])
	}
	if daily, _ := s.History("k"); daily[0].Models["priced-a"].Cost < 2.399 {
		t.Fatalf("history not backfilled: %+v", daily[0].Models["priced-a"])
	}
	if r := s.Recent(0); r[len(r)-1].Cost < 2.399 {
		t.Fatalf("recent not backfilled: %+v", r[len(r)-1])
	}

	// Runs once: prices that change later do not re-price stored usage.
	cfg.ModelPrices[0].Input = 100
	s.BackfillCosts()
	if c := s.Usage("k")[Day].Models["priced-a"].Cost; c > 2.401 {
		t.Fatalf("backfill ran twice: %v", c)
	}
	s.Flush()
	restored := newTestStore(now)
	restored.path = path
	restored.load()
	if !restored.costsBackfilled {
		t.Fatal("backfill flag not persisted")
	}
}
