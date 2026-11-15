package pricing

import (
	"math"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const testCatalog = `{
 "anthropic": {"id":"anthropic","models":{
   "claude-sonnet-4-5-20250929":{"cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}},
   "claude-opus-5":{"cost":{"input":5,"output":25,"cache_read":0.5,"cache_write":6.25}}}},
 "openai": {"id":"openai","models":{
   "gpt-5":{"cost":{"input":2.5,"output":10,"cache_read":1.25}},
   "gpt-4o-mini":{"cost":{"input":0.15,"output":0.6}}}},
 "google": {"id":"google","models":{"gemini-2.5-pro":{"cost":{"input":1.25,"output":10,"cache_read":0.125}}}},
 "openrouter": {"id":"openrouter","models":{
   "openai/gpt-5":{"cost":{"input":2.6,"output":11}},
   "meta-llama/llama-3.3-70b-instruct":{"cost":{"input":0.1,"output":0.3}}}},
 "groq": {"id":"groq","models":{"llama-3.3-70b-versatile":{"cost":{"input":0.59,"output":0.79}}}},
 "resellerz": {"id":"resellerz","models":{"only-here":{"cost":{"input":9,"output":9}}}}
}`

func f(v float64) *float64 { return &v }

func setup(t *testing.T, cfg config.ProxxPricingConfig) *Catalog {
	t.Helper()
	c, err := ParseCatalog([]byte(testCatalog))
	if err != nil {
		t.Fatal(err)
	}
	prevState, prevCat := stateValue.Load(), catalogValue.Load()
	t.Cleanup(func() { stateValue.Store(prevState); catalogValue.Store(prevCat) })
	Configure(nil, cfg)
	catalogValue.Store(&catalogInfo{catalog: c, source: "test"})
	return c
}

func near(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("cost = nil, want %v", want)
	}
	if math.Abs(*got-want) > 1e-12 {
		t.Fatalf("cost = %.12f, want %.12f", *got, want)
	}
}

func TestLookupProviderMapping(t *testing.T) {
	setup(t, config.ProxxPricingConfig{})
	cases := []struct {
		provider, model string
		input           float64
	}{
		{"claude", "claude-sonnet-4-5-20250929", 3},
		{"claude", "claude-sonnet-4-5", 3},                            // date-insensitive
		{"claude", "claude-opus-5-thinking", 5},                       // variant suffix
		{"antigravity", "claude-opus-5", 5},                           // any-provider fallback
		{"gemini", "models/gemini-2.5-pro", 1.25},                     // models/ prefix
		{"vertex", "gemini-2.5-pro(high)", 1.25},                      // thinking suffix
		{"codex", "gpt-5", 2.5},                                       // codex → openai
		{"openrouter", "openai/gpt-5", 2.6},                           // compat name → same models.dev ID
		{"groq", "llama-3.3-70b-versatile", 0.59},                     // compat groq
		{"some-compat", "openai/gpt-4o-mini", 0.15},                   // vendor/model → vendor catalog
		{"some-compat", "only-here", 9},                               // last resort: any provider
		{"openrouter", "meta-llama/llama-3.3-70b-instruct:free", 0.1}, // exact first? no → see free test
	}
	for _, tc := range cases[:len(cases)-1] {
		m, ok := Lookup(tc.provider, tc.model)
		if !ok || m.Price.Input == nil || *m.Price.Input != tc.input || m.Source != SourceCatalog {
			t.Errorf("Lookup(%q,%q) = %+v ok=%v, want input %v", tc.provider, tc.model, m, ok, tc.input)
		}
	}
	if _, ok := Lookup("openai", "no-such-model"); ok {
		t.Fatal("unknown model must not resolve")
	}
}

func TestLookupFreeIsZero(t *testing.T) {
	setup(t, config.ProxxPricingConfig{})
	m, ok := Lookup("openrouter", "meta-llama/llama-3.3-70b-instruct:free")
	if !ok || m.Price.Input == nil || *m.Price.Input != 0 || *m.Price.Output != 0 {
		t.Fatalf(":free must price at explicit 0, got %+v ok=%v", m, ok)
	}
}

func TestLookupConfigOverrides(t *testing.T) {
	setup(t, config.ProxxPricingConfig{Models: []config.ProxxModelPrice{
		{Model: "gpt-5", Input: f(1)},                         // partial: output falls back to catalog
		{Model: "gpt-5", Provider: "openrouter", Input: f(7)}, // provider-restricted wins
		{Model: "my-*", Input: f(2), Output: f(3)},
		{Model: "my-long-*", Input: f(4), Output: f(5), Override: true},
	}})
	m, _ := Lookup("codex", "gpt-5")
	if *m.Price.Input != 1 || *m.Price.Output != 10 || m.Source != SourceConfig {
		t.Fatalf("partial override = %+v", m)
	}
	m, _ = Lookup("openrouter", "gpt-5")
	if *m.Price.Input != 7 {
		t.Fatalf("provider-restricted override = %+v", m)
	}
	m, _ = Lookup("x", "my-long-model")
	if *m.Price.Input != 4 || !m.Override {
		t.Fatalf("longest prefix = %+v", m)
	}
	m, _ = Lookup("x", "my-model")
	if *m.Price.Input != 2 || m.Override {
		t.Fatalf("prefix = %+v", m)
	}
}

// Claude reports input_tokens EXCLUDING cache reads/writes.
func claudeRecord() usage.Record {
	return usage.Record{
		Provider: "claude", ExecutorType: "ClaudeExecutor", Model: "claude-sonnet-4-5-20250929", Alias: "auto",
		Detail: usage.Detail{InputTokens: 100, CacheReadTokens: 1000, CacheCreationTokens: 200, CachedTokens: 1000, OutputTokens: 50},
	}
}

// OpenAI reports prompt_tokens INCLUDING cached tokens and completion tokens INCLUDING reasoning.
func openAIRecord() usage.Record {
	return usage.Record{
		Provider: "codex", ExecutorType: "CodexExecutor", Model: "gpt-5",
		Detail: usage.Detail{InputTokens: 1100, CachedTokens: 1000, CacheReadTokens: 1000, OutputTokens: 500, ReasoningTokens: 200, TotalTokens: 1600},
	}
}

func TestCostClaudeCacheTokens(t *testing.T) {
	setup(t, config.ProxxPricingConfig{})
	rec := claudeRecord()
	ApplyCost(nil, &rec)
	// 100×3 + 1000×0.3 + 200×3.75 + 50×15 = 2100 per 1M
	near(t, rec.Detail.CostDetails.FinalCost, 2100.0/1e6)
	if rec.Detail.CostDetails.CostSource != SourceCatalog {
		t.Fatalf("source = %q", rec.Detail.CostDetails.CostSource)
	}
}

func TestCostOpenAICacheAndReasoning(t *testing.T) {
	setup(t, config.ProxxPricingConfig{})
	rec := openAIRecord()
	ApplyCost(nil, &rec)
	// uncached 100×2.5 + cached 1000×1.25 + output 500×10 (reasoning inside output) = 6500
	near(t, rec.Detail.CostDetails.FinalCost, 6500.0/1e6)

	setup(t, config.ProxxPricingConfig{Models: []config.ProxxModelPrice{{Model: "gpt-5", Reasoning: f(20)}}})
	rec = openAIRecord()
	ApplyCost(nil, &rec)
	// 250 + 1250 + 300×10 + 200×20 = 8500
	near(t, rec.Detail.CostDetails.FinalCost, 8500.0/1e6)
	if rec.Detail.CostDetails.CostSource != SourceConfig {
		t.Fatalf("source = %q", rec.Detail.CostDetails.CostSource)
	}
}

func TestCostPriority(t *testing.T) {
	setup(t, config.ProxxPricingConfig{CostHeaders: []string{"X-Litellm-Response-Cost"}})
	upstream := func() usage.Record {
		rec := openAIRecord()
		rec.Detail.CostDetails = &usage.CostDetails{Cost: f(0.42)}
		return rec
	}
	rec := upstream()
	orig := rec.Detail.CostDetails
	ApplyCost(nil, &rec)
	near(t, rec.Detail.CostDetails.FinalCost, 0.42)
	if rec.Detail.CostDetails.CostSource != SourceUpstream || orig.FinalCost != nil {
		t.Fatalf("upstream cost: %+v (original mutated: %v)", rec.Detail.CostDetails, orig.FinalCost != nil)
	}

	rec = openAIRecord()
	rec.Detail.CostDetails = &usage.CostDetails{CostInUSDTicks: usage.Int64Ptr(12_345_000_000)}
	ApplyCost(nil, &rec)
	near(t, rec.Detail.CostDetails.FinalCost, 1.2345)

	rec = openAIRecord()
	rec.ResponseHeaders = http.Header{"X-Litellm-Response-Cost": {"0.0777"}}
	ApplyCost(nil, &rec)
	near(t, rec.Detail.CostDetails.FinalCost, 0.0777)
	if rec.Detail.CostDetails.CostSource != SourceHeader {
		t.Fatalf("source = %q", rec.Detail.CostDetails.CostSource)
	}

	// override-upstream: catalog price beats the upstream-reported cost.
	setup(t, config.ProxxPricingConfig{OverrideUpstream: true})
	rec = upstream()
	ApplyCost(nil, &rec)
	near(t, rec.Detail.CostDetails.FinalCost, 6500.0/1e6)

	// per-model override only.
	setup(t, config.ProxxPricingConfig{Models: []config.ProxxModelPrice{{Model: "gpt-5", Input: f(1), Output: f(1), CacheRead: f(1), Override: true}}})
	rec = upstream()
	ApplyCost(nil, &rec)
	near(t, rec.Detail.CostDetails.FinalCost, 1600.0/1e6)
	if rec.Detail.CostDetails.CostSource != SourceConfig {
		t.Fatalf("source = %q", rec.Detail.CostDetails.CostSource)
	}
}

func TestCostUnknownAndDisabled(t *testing.T) {
	setup(t, config.ProxxPricingConfig{})
	rec := openAIRecord()
	rec.Model = "mystery-model"
	ApplyCost(nil, &rec)
	if rec.Detail.CostDetails.FinalCost != nil || rec.Detail.CostDetails.CostSource != SourceUnknown {
		t.Fatalf("unknown model: %+v", rec.Detail.CostDetails)
	}
	// Response model is used when the request model is unknown.
	rec = openAIRecord()
	rec.Model, rec.ResponseModel = "mystery-model", "gpt-4o-mini"
	ApplyCost(nil, &rec)
	if rec.Detail.CostDetails.FinalCost == nil {
		t.Fatal("response model fallback failed")
	}

	setup(t, config.ProxxPricingConfig{Disabled: true})
	rec = openAIRecord()
	ApplyCost(nil, &rec)
	if rec.Detail.CostDetails != nil {
		t.Fatalf("disabled pricing must not touch the record: %+v", rec.Detail.CostDetails)
	}
}

func TestEmbeddedSnapshotParses(t *testing.T) {
	c, err := ParseCatalog(embeddedSnapshot)
	if err != nil || c.Len() < 100 {
		t.Fatalf("snapshot: %v (%d models)", err, c.Len())
	}
	if _, ok := lookupCatalog(c, "claude", "claude-sonnet-4-5-20250929"); !ok {
		t.Fatal("snapshot misses a registry Claude model")
	}
}
