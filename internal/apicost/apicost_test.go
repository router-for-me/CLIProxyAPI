package apicost

import (
	"context"
	"math"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const testCatalogJSON = `{
	"sample_spec": {"input_cost_per_token": 0, "output_cost_per_token": 0},
	"claude-test": {
		"litellm_provider": "anthropic",
		"input_cost_per_token": 0.000004,
		"output_cost_per_token": 0.00002,
		"cache_creation_input_token_cost": 0.000005,
		"cache_read_input_token_cost": 0.0000002,
		"provider_specific_entry": {"us": 1.1, "fast": 2}
	},
	"claude-tiered": {
		"litellm_provider": "anthropic",
		"input_cost_per_token": 1,
		"output_cost_per_token": 10,
		"input_cost_per_token_above_200k_tokens": 2
	},
	"claude-defaults": {
		"litellm_provider": "anthropic",
		"input_cost_per_token": 1,
		"output_cost_per_token": 10
	},
	"gpt-test": {
		"litellm_provider": "openai",
		"input_cost_per_token": 0.000002,
		"output_cost_per_token": 0.00001,
		"cache_creation_input_token_cost": 0.0000025,
		"cache_creation_input_token_cost_above_12hr": 0.000005,
		"cache_read_input_token_cost": 0.0000002,
		"input_cost_per_token_above_272k_tokens": 0.000004,
		"output_cost_per_token_above_272k_tokens": 0.000015,
		"cache_read_input_token_cost_above_272k_tokens": 0.0000004
	},
	"gpt-6.1-sol": {
		"litellm_provider": "openai",
		"input_cost_per_token": 1,
		"output_cost_per_token": 1
	},
	"gpt-nocache": {
		"litellm_provider": "openai",
		"input_cost_per_token": 1,
		"output_cost_per_token": 1
	},
	"gemini/gemini-test": {
		"litellm_provider": "gemini",
		"input_cost_per_token": 1,
		"output_cost_per_token": 2
	},
	"no-output": {"input_cost_per_token": 1}
}`

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, errParse := ParseLiteLLM([]byte(testCatalogJSON))
	if errParse != nil {
		t.Fatalf("ParseLiteLLM: %v", errParse)
	}
	return catalog
}

func mustPrice(t *testing.T, catalog *Catalog, model string) Price {
	t.Helper()
	price, _, ok := catalog.Lookup(model)
	if !ok {
		t.Fatalf("Lookup(%q) found nothing", model)
	}
	return price
}

func near(got, want float64) bool { return math.Abs(got-want) <= 1e-9*math.Max(1, math.Abs(want)) }

func TestParseLiteLLMDefaultsAndMultipliers(t *testing.T) {
	catalog := testCatalog(t)
	if _, _, ok := catalog.Lookup("no-output"); ok {
		t.Fatal("entry without output price must be skipped")
	}
	if _, _, ok := catalog.Lookup("sample_spec"); ok {
		t.Fatal("sample_spec must be skipped")
	}
	defaults := mustPrice(t, catalog, "claude-defaults")
	if !near(defaults.CacheWrite, 1.25) || !near(defaults.CacheRead, 0.1) {
		t.Fatalf("anthropic defaults = %+v, want cache write 1.25x and read 0.1x input", defaults)
	}
	if price := mustPrice(t, catalog, "gpt-nocache"); !near(price.CacheRead, 1) {
		t.Fatalf("openai cache read default = %v, want input rate", price.CacheRead)
	}
	if price := mustPrice(t, catalog, "claude-test"); price.Fast != 2 {
		t.Fatalf("fast multiplier = %v, want provider_specific_entry.fast", price.Fast)
	}
	sol := mustPrice(t, catalog, "gpt-6.1-sol")
	if sol.Fast != 2 || sol.Flex != 0.5 {
		t.Fatalf("gpt-6.1-sol multipliers = %v/%v, want ccusage overrides 2/0.5", sol.Fast, sol.Flex)
	}
}

func TestLookupNormalizesModelNames(t *testing.T) {
	catalog := testCatalog(t)
	cases := map[string]string{
		"Claude-Test":               "claude-test",
		"claude-test-20261101":      "claude-test",
		"claude-test(high)":         "claude-test",
		"claude-test-thinking":      "claude-test",
		"models/gemini-test":        "gemini-test",
		"gemini-test":               "gemini-test",
		"claude-test-thinking(max)": "claude-test",
	}
	for model, want := range cases {
		if _, got, ok := catalog.Lookup(model); !ok || got != want {
			t.Fatalf("Lookup(%q) = %q, %v; want %q", model, got, ok, want)
		}
	}
	if _, _, ok := catalog.Lookup("unknown-model"); ok {
		t.Fatal("unknown model must not match")
	}
}

func TestCostAnthropicBuckets(t *testing.T) {
	price := mustPrice(t, testCatalog(t), "claude-test")
	usage := Usage{Input: 1000, CacheRead: 100000, CacheWrite: 3000, CacheWrite1h: 1000, Output: 2000}
	// input 1000*4e-6 + output 2000*2e-5 + 5m write 2000*5e-6
	// + 1h write 1000*(2*4e-6) + read 100000*2e-7
	want := 0.004 + 0.04 + 0.01 + 0.008 + 0.02
	if got := Cost(price, usage, TierStandard); !near(got, want) {
		t.Fatalf("standard cost = %v, want %v", got, want)
	}
	if got := Cost(price, usage, TierFast); !near(got, 2*want) {
		t.Fatalf("fast cost = %v, want %v", got, 2*want)
	}
}

func TestCostAnthropicMarginalAbove200k(t *testing.T) {
	price := mustPrice(t, testCatalog(t), "claude-tiered")
	got := Cost(price, Usage{Input: 250_000, CacheWrite: 250_000, CacheWrite1h: 250_000}, TierStandard)
	// input: 200k*1 + 50k*2; 1h write: 200k*2 + 50k*4
	want := 300_000.0 + 600_000.0
	if !near(got, want) {
		t.Fatalf("tiered cost = %v, want %v", got, want)
	}
}

func TestCostOpenAILongContextAndTiers(t *testing.T) {
	price := mustPrice(t, testCatalog(t), "gpt-test")
	short := Usage{Input: 100_000, CacheRead: 100_000, CacheWrite: 20_000, CacheWrite12h: 10_000, Output: 1000}
	wantShort := 100_000*0.000002 + 100_000*0.0000002 + 10_000*0.0000025 + 10_000*0.000005 + 1000*0.00001
	if got := Cost(price, short, TierStandard); !near(got, wantShort) {
		t.Fatalf("short cost = %v, want %v", got, wantShort)
	}
	long := Usage{Input: 100_000, CacheRead: 200_000, Output: 1000}
	// Whole request at long-context rates once input exceeds 272k.
	wantLong := 100_000*0.000004 + 200_000*0.0000004 + 1000*0.000015
	if got := Cost(price, long, TierStandard); !near(got, wantLong) {
		t.Fatalf("long cost = %v, want %v", got, wantLong)
	}
	sol := mustPrice(t, testCatalog(t), "gpt-6.1-sol")
	if got := Cost(sol, Usage{Input: 10, Output: 10}, TierFlex); !near(got, 10) {
		t.Fatalf("flex cost = %v, want 10", got)
	}
}

func TestStoreHandleUsageAggregatesAndFlagsUnpriced(t *testing.T) {
	catalog := testCatalog(t)
	store := NewStore()
	store.catalog = func() *Catalog { return catalog }
	at := time.Date(2026, 11, 16, 10, 30, 0, 0, time.UTC)
	record := func(model string, input, output int64) coreusage.Record {
		return coreusage.Record{
			Provider:    "claude",
			Model:       model,
			AuthID:      "auth-1",
			RequestedAt: at,
			Detail:      coreusage.Detail{TokenBreakdown: coreusage.NewIndependentTokenBreakdown(input, 0, 0, output, 0, 0)},
		}
	}
	store.HandleUsage(context.Background(), record("claude-test", 1000, 100))
	store.HandleUsage(context.Background(), record("claude-test-20261101", 1000, 100))
	fast := record("claude-test", 1000, 100)
	fast.Detail.Speed = "fast"
	store.HandleUsage(context.Background(), fast)
	store.HandleUsage(context.Background(), record("mystery-model", 50, 5))
	store.HandleUsage(context.Background(), record("claude-test", 0, 0))

	buckets := store.Buckets(at.Add(-time.Hour))
	if len(buckets) != 2 {
		t.Fatalf("buckets = %+v, want claude-test and mystery-model", buckets)
	}
	priced := buckets[0]
	perRequest := 1000*0.000004 + 100*0.00002
	if priced.Model != "claude-test" || priced.Requests != 3 || priced.InputTokens != 3000 || !near(priced.CostUSD, 4*perRequest) {
		t.Fatalf("priced bucket = %+v, want 3 requests costing %v", priced, 4*perRequest)
	}
	if !priced.Hour.Equal(at.Truncate(time.Hour)) {
		t.Fatalf("bucket hour = %v", priced.Hour)
	}
	unpriced := buckets[1]
	if unpriced.Model != "mystery-model" || unpriced.UnpricedRequests != 1 || unpriced.CostUSD != 0 {
		t.Fatalf("unpriced bucket = %+v", unpriced)
	}
	if !store.Dirty() {
		t.Fatal("store must be dirty after usage")
	}
	if exported := store.Export(); len(exported) != 2 || store.Dirty() {
		t.Fatalf("export = %d buckets, dirty %v", len(exported), store.Dirty())
	}
	store.Prune(at.Add(DefaultRetention + 2*time.Hour))
	if got := store.Buckets(time.Time{}); len(got) != 0 {
		t.Fatalf("buckets after prune = %+v", got)
	}
}

func TestEmbeddedSnapshotPricesCurrentModels(t *testing.T) {
	catalog, errParse := ParseLiteLLM(embeddedPricing)
	if errParse != nil {
		t.Fatalf("embedded snapshot: %v", errParse)
	}
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "gpt-6.1-sol"} {
		if _, _, ok := catalog.Lookup(model); !ok {
			t.Fatalf("embedded snapshot has no price for %s", model)
		}
	}
}
