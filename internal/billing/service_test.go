package billing

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestBillingLegacyTokenPricingAndSummary(t *testing.T) {
	svc := NewService(Config{Enabled: true, Mode: "observe", LedgerPath: t.TempDir() + "/billing.jsonl", Token: TokenConfig{LegacyAPIKeys: "observe"}, PriceBook: PriceBookConfig{Version: "test", Rules: []PriceRule{{ID: "rule", BillingProvider: "openai", Model: "gpt-5.5", InputUncachedNanosPerToken: 5000, InputCacheReadNanosPerToken: 500, OutputTextNanosPerToken: 30000, OutputReasoningNanosPerToken: 30000}}}}, []string{"sk-test"})
	p, ok := svc.AuthenticateToken("sk-test")
	if !ok || p.UserID != "legacy" || p.TokenID == "" {
		t.Fatalf("legacy principal = %+v ok=%v", p, ok)
	}
	ctx := WithPrincipal(context.Background(), p)
	svc.HandleUsage(ctx, coreusage.Record{Provider: "openai", Model: "gpt-5.5", RequestedAt: time.Now(), Detail: coreusage.Detail{InputTokens: 100000, CacheReadTokens: 80000, OutputTokens: 5000, TotalTokens: 105000}})
	summary := svc.Summary()
	if summary.Requests != 1 || summary.PricedRequests != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	// 20k uncached*$5/1M + 80k cached*$0.5/1M + 5k output*$30/1M = $0.29 = 290,000,000 nanos.
	if summary.CustomerCostNanos != 290000000 {
		t.Fatalf("cost nanos = %d, want 290000000", summary.CustomerCostNanos)
	}
}

func TestBillingQuotaSurvivesServiceRestartFromLedger(t *testing.T) {
	ledger := t.TempDir() + "/billing.jsonl"
	cfg := Config{Enabled: true, Mode: "observe", LedgerPath: ledger, Token: TokenConfig{LegacyAPIKeys: "observe"}, Quota: QuotaConfig{DailyNanos: 1}, PriceBook: PriceBookConfig{Version: "test", Rules: []PriceRule{{ID: "rule", BillingProvider: "openai", Model: "gpt-5.5", InputUncachedNanosPerToken: 5000, OutputTextNanosPerToken: 30000}}}}
	svc := NewService(cfg, []string{"sk-test"})
	p, ok := svc.AuthenticateToken("sk-test")
	if !ok {
		t.Fatal("legacy token did not authenticate")
	}
	svc.HandleUsage(WithPrincipal(context.Background(), p), coreusage.Record{Provider: "openai", Model: "gpt-5.5", RequestedAt: time.Now(), Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}})

	cfg.Mode = "enforce-soft"
	restarted := NewService(cfg, []string{"sk-test"})
	if _, err := restarted.acquire(p); err == nil {
		t.Fatal("expected persisted daily quota to reject after restart")
	}
}
