package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func priceString(s string) *string { return &s }

func testPriceBook(t *testing.T, rates ...config.PriceRate) *PriceBook {
	t.Helper()
	b, err := NewPriceBook(rates, nil, makeSources(len(rates), "test"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPriceBucketsAndPrecision(t *testing.T) {
	rate := config.PriceRate{Provider: "openai", Model: "actual", Currency: "USD", Input: priceString("0.000001"), Output: priceString("0.000002"), CacheRead: priceString("0.0000005"), CacheCreation: priceString("0.000003")}
	event := AccountingEvent{Provider: "openai", ExecutedModel: "actual", Tokens: &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(100, 40, 10, 50, 20, 150)}}
	e := testPriceBook(t, rate).Estimate(event)
	if e.Status != "priced" || e.Amount == nil || *e.Amount != "0.000200000" {
		t.Fatalf("%+v", e)
	}

	rate.Input = priceString("0.0000000005")
	event.Tokens.Breakdown = NewSubsetTokenBreakdown(1, 0, 0, 0, 0, 1)
	e = testPriceBook(t, rate).Estimate(event)
	if *e.Amount != "0.000000001" {
		t.Fatalf("half-up rounding: %s", *e.Amount)
	}
	rate.Input = priceString("0.000000000499999999")
	e = testPriceBook(t, rate).Estimate(event)
	if *e.Amount != "0.000000000" {
		t.Fatal(*e.Amount)
	}
}

func TestPriceResolution(t *testing.T) {
	rates := []config.PriceRate{
		{Provider: "openai", Model: "actual", Currency: "USD", Input: priceString("1")},
		{Provider: "openai", Model: "actual", Tier: "priority", MinContext: 101, Currency: "USD", Input: priceString("2")},
	}
	b, err := NewPriceBook(rates, []config.PriceAlias{{Provider: "codex", Model: "reported", TargetProvider: "openai", TargetModel: "actual"}}, makeSources(2, "test"))
	if err != nil {
		t.Fatal(err)
	}
	event := AccountingEvent{Provider: "codex", RequestedAlias: "requested", ExecutedModel: "executed", ResponseModel: "reported", RequestedServiceTier: "flex", ReportedServiceTier: "priority", Tokens: &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(101, 0, 0, 0, 0, 101)}}
	e := b.Estimate(event)
	if e.Status != "priced" || *e.Amount != "202.000000000" || e.Model != "actual" || event.ExecutedModel != "executed" || event.RequestedServiceTier != "flex" {
		t.Fatalf("%+v", e)
	}
	event.Tokens.Breakdown = NewSubsetTokenBreakdown(100, 0, 0, 0, 0, 100)
	if e = b.Estimate(event); e.Status != "unpriced" {
		t.Fatalf("%+v", e)
	}
	event.ReportedServiceTier = ""
	if e = b.Estimate(event); *e.Amount != "100.000000000" {
		t.Fatalf("%+v", e)
	}
}

func TestUnpricedAndPartial(t *testing.T) {
	b := testPriceBook(t, config.PriceRate{Provider: "openai", Model: "m", Currency: "USD", Input: priceString("1")})
	event := AccountingEvent{Provider: "openai", ExecutedModel: "m"}
	if e := b.Estimate(event); e.Status != "unpriced" || e.Amount != nil {
		t.Fatalf("%+v", e)
	}
	event.Tokens = &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(1, 0, 0, 1, 0, 2)}
	if e := b.Estimate(event); e.Status != "partial" || *e.Amount != "1.000000000" {
		t.Fatalf("%+v", e)
	}
	event.ExecutedModel = "unknown"
	if e := b.Estimate(event); e.Status != "unpriced" || e.Amount != nil {
		t.Fatalf("%+v", e)
	}
	event.ExecutedModel = "m"
	event.Tokens.Breakdown = NewUnclassifiedTokenBreakdown(12)
	if e := b.Estimate(event); e.Status != "unpriced" || e.Amount != nil {
		t.Fatalf("%+v", e)
	}
}

func TestCatalogCacheAndOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	raw := []byte(`{"openai/m":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":1e-6,"output_cost_per_token":0.000002}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.PricingConfig{LiteLLMCatalogPath: path}
	first := loadPriceBook(cfg, dir)
	if first == nil {
		t.Fatal("no imported book")
	}
	if err := os.WriteFile(path, []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	cached := loadPriceBook(cfg, dir)
	if cached == nil || cached.snapshot != first.snapshot {
		t.Fatal("cache fallback changed snapshot")
	}
	cfg.Overrides = []config.PriceRate{{Provider: "openai", Model: "m", Currency: "USD", Input: priceString("3")}}
	override := loadPriceBook(cfg, dir)
	event := AccountingEvent{Provider: "openai", ExecutedModel: "m", Tokens: &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(1, 0, 0, 0, 0, 1)}}
	e := override.Estimate(event)
	if e.Source != "local_override" || *e.Amount != "3.000000000" || e.Snapshot == first.snapshot {
		t.Fatalf("%+v", e)
	}
	if _, err := importLiteLLM([]byte(`{"m":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":-1}}`)); err == nil {
		t.Fatal("negative catalog price accepted")
	}
}

func TestPersistedEstimateSnapshot(t *testing.T) {
	cfg := config.AccountingOutboxConfig{DataPath: t.TempDir(), Pricing: config.PricingConfig{Overrides: []config.PriceRate{{Provider: "openai", Model: "m", Currency: "USD", Input: priceString("1")}}}}
	o, err := OpenOutbox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	event := AccountingEvent{SchemaVersion: AccountingEventSchemaVersion, ExecutionID: "priced-event", CompletedAt: time.Now(), Provider: "openai", ExecutedModel: "m", Tokens: &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(1, 0, 0, 0, 0, 1)}}
	o.HandleAccountingEvent(event)
	if err := o.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Pricing.Overrides[0].Input = priceString("2")
	reopened, err := OpenOutbox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	stored, err := reopened.Event(event.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Estimate == nil || *stored.Estimate.Amount != "1.000000000" || stored.Estimate.Snapshot == reopened.prices.snapshot {
		t.Fatalf("%+v", stored.Estimate)
	}
	claimed, err := reopened.Claim(event.ExecutionID)
	if err != nil || !claimed {
		t.Fatal("claim", err)
	}
	if err := reopened.Resolve(event.ExecutionID, 1, DeliveryRetryable, time.Time{}); err != nil {
		t.Fatal(err)
	}
	claimed, err = reopened.Claim(event.ExecutionID)
	if err != nil || !claimed {
		t.Fatal("retry claim", err)
	}
	retried, err := reopened.Event(event.ExecutionID)
	if err != nil || *retried.Estimate.Amount != *stored.Estimate.Amount || retried.Estimate.Snapshot != stored.Estimate.Snapshot {
		t.Fatal("retry changed estimate", err)
	}
	raw, err := json.Marshal(stored.Estimate)
	if err != nil {
		t.Fatal(err)
	}
	var restored CostEstimate
	if err := json.Unmarshal(raw, &restored); err != nil || *restored.Amount != "1.000000000" {
		t.Fatal("estimate round trip failed", err)
	}
}

func TestIndependentReasoningAndSnapshotIsolation(t *testing.T) {
	rate := config.PriceRate{Provider: "claude", Model: "m", Currency: "USD", Input: priceString("1"), Output: priceString("2"), CacheRead: priceString("0.5"), CacheCreation: priceString("3"), Reasoning: priceString("4")}
	b := testPriceBook(t, rate)
	*rate.Input = "99"
	event := AccountingEvent{Provider: "claude", ExecutedModel: "m", Tokens: &AccountingTokens{Breakdown: NewIndependentTokenBreakdown(1, 2, 3, 4, 5, 15)}}
	first := b.Estimate(event)
	if first.Status != "priced" || *first.Amount != "39.000000000" {
		t.Fatalf("%+v", first)
	}
	*first.Rates.Input = "100"
	second := b.Estimate(event)
	if *second.Amount != "39.000000000" {
		t.Fatal("estimate mutated price snapshot")
	}
}

func TestInvalidPriceSnapshot(t *testing.T) {
	for _, value := range []string{"-1", "NaN", "1/2", "1e-6"} {
		_, err := NewPriceBook([]config.PriceRate{{Provider: "p", Model: "m", Currency: "USD", Input: priceString(value)}}, nil, []string{"test"})
		if err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

func TestUnsupportedCatalogBandsRemainUnpriced(t *testing.T) {
	rates, err := importLiteLLM([]byte(`{"m":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000001,"input_cost_per_token_above_200k_tokens":0.000002}}`))
	if err != nil {
		t.Fatal(err)
	}
	event := AccountingEvent{Provider: "openai", ExecutedModel: "m", Tokens: &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(200001, 0, 0, 0, 0, 200001)}}
	e := testPriceBook(t, rates...).Estimate(event)
	if e.Status != "unpriced" || e.Amount != nil {
		t.Fatalf("%+v", e)
	}
}
