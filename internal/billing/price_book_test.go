package billing

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestDefaultPriceBookMainstreamModels(t *testing.T) {
	rules := defaultPriceRules(PriceBookConfig{})
	byModel := map[string]PriceRule{}
	for _, r := range rules {
		if _, exists := byModel[r.Model]; exists {
			t.Fatalf("duplicate default price rule for model %q", r.Model)
		}
		byModel[r.Model] = r
		if r.BillingProvider == "claude" && r.InputCacheWrite1hNanosPerToken != 2*r.InputUncachedNanosPerToken {
			t.Fatalf("missing Claude 1-hour cache pricing: %+v", r)
		}
	}

	want := map[string]struct {
		provider  string
		input     int64
		cacheRead int64
		cache5m   int64
		output    int64
	}{
		"claude-opus-4-5-20251101":   {"claude", 5000, 500, 6250, 25000},
		"gpt-5.6-sol":                {"openai", 4000, 400, 5000, 20000},
		"gpt-5.6-terra":              {"openai", 2000, 200, 2500, 12000},
		"gpt-5.6-luna":               {"openai", 200, 20, 250, 1200},
		"gpt-4.1":                    {"openai", 2000, 500, 0, 8000},
		"claude-sonnet-4.5":          {"claude", 3000, 300, 3750, 15000},
		"claude-sonnet-4-5-20250929": {"claude", 3000, 300, 3750, 15000},
		"claude-haiku-4-5-20251001":  {"claude", 1000, 100, 1250, 5000},
		"gemini-2.5-pro":             {"gemini", 1250, 125, 0, 10000},
		"gemini-2.5-flash":           {"gemini", 300, 30, 0, 2500},
		"grok-4.6":                   {"xai", 2000, 500, 0, 6000},
		"deepseek-flash":             {"deepseek", 300, 6, 0, 1200},
	}
	for model, w := range want {
		r, ok := byModel[model]
		if !ok {
			t.Fatalf("missing default price rule for %s", model)
		}
		if r.BillingProvider != w.provider || r.InputUncachedNanosPerToken != w.input || r.InputCacheReadNanosPerToken != w.cacheRead || r.InputCacheWrite5mNanosPerToken != w.cache5m || r.OutputTextNanosPerToken != w.output || r.OutputReasoningNanosPerToken != w.output {
			t.Fatalf("rule for %s = %+v, want provider=%s input=%d cacheRead=%d cache5m=%d output=%d", model, r, w.provider, w.input, w.cacheRead, w.cache5m, w.output)
		}
	}

	if len(rules) != 88 {
		t.Fatalf("default rule coverage = %d, want 88 explicit model IDs", len(rules))
	}
}

func TestDefaultPriceBookUnknownModelRemainsUnpriced(t *testing.T) {
	svc := NewService(Config{Enabled: true, Mode: "observe", LedgerPath: t.TempDir() + "/billing.jsonl", Token: TokenConfig{LegacyAPIKeys: "observe"}}, []string{"sk-test"})
	p, ok := svc.AuthenticateToken("sk-test")
	if !ok {
		t.Fatal("legacy token did not authenticate")
	}

	svc.HandleUsage(WithPrincipal(context.Background(), p), coreusage.Record{RequestID: "unknown-model", Provider: "openai", Model: "not-a-priced-model", RequestedAt: time.Now(), Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}})
	summary := svc.Summary()
	if summary.Requests != 1 || summary.PricedRequests != 0 || summary.UnpricedRequests != 1 || summary.CustomerCostNanos != 0 {
		t.Fatalf("summary for unknown model = %+v, want one unpriced request", summary)
	}
	events := svc.Events()
	if len(events) != 1 || events[0].BillingQuality != "unpriced" || events[0].CustomerCostNanos != nil || events[0].PriceRuleID != "" {
		t.Fatalf("event for unknown model = %+v, want unpriced with no price rule", events)
	}
}

func TestCustomPriceRulesReplaceDefaultsAndOverrideModel(t *testing.T) {
	custom := PriceRule{ID: "rule", BillingProvider: "openai", Model: "gpt-5.6-sol", ServiceTier: "standard", InputUncachedNanosPerToken: 1, InputCacheReadNanosPerToken: 2, OutputTextNanosPerToken: 3, OutputReasoningNanosPerToken: 3}
	svc := NewService(Config{Enabled: true, Mode: "observe", LedgerPath: t.TempDir() + "/billing.jsonl", Token: TokenConfig{LegacyAPIKeys: "observe"}, PriceBook: PriceBookConfig{Version: "test", Rules: []PriceRule{custom}}}, []string{"sk-test"})
	p, ok := svc.AuthenticateToken("sk-test")
	if !ok {
		t.Fatal("legacy token did not authenticate")
	}

	svc.HandleUsage(WithPrincipal(context.Background(), p), coreusage.Record{RequestID: "custom-overrides-default", Provider: "openai", Model: "gpt-5.6-sol", RequestedAt: time.Now(), Detail: coreusage.Detail{InputTokens: 10, CacheReadTokens: 4, OutputTokens: 7, TotalTokens: 17}})
	svc.HandleUsage(WithPrincipal(context.Background(), p), coreusage.Record{RequestID: "default-not-inherited", Provider: "openai", Model: "gpt-5.6-terra", RequestedAt: time.Now(), Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}})

	summary := svc.Summary()
	if summary.Requests != 2 || summary.PricedRequests != 1 || summary.UnpricedRequests != 1 {
		t.Fatalf("summary = %+v, want one priced custom request and one unpriced replaced default", summary)
	}
	// Input uncached is input minus cache read: 6*1 + 4*2 + 7*3 = 35 nanos.
	if summary.CustomerCostNanos != 35 {
		t.Fatalf("custom override cost = %d, want 35", summary.CustomerCostNanos)
	}
	e, ok := svc.EventByRequestID("custom-overrides-default")
	if !ok {
		t.Fatal("missing custom override event")
	}
	if e.PriceRuleID != "rule" || e.PriceSnapshot == nil || e.PriceSnapshot.ID != "rule" {
		t.Fatalf("custom override event = %+v, want preserved custom rule ID", e)
	}
	e, ok = svc.EventByRequestID("default-not-inherited")
	if !ok || e.CustomerCostNanos != nil || e.BillingQuality != "unpriced" {
		t.Fatalf("replacement semantics event = %+v ok=%v, want default model unpriced", e, ok)
	}
}
