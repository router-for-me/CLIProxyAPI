package pricing

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func jevRecord(model string) usage.Record {
	return usage.Record{
		Provider: "typesafe",
		Model:    model,
		Detail: usage.Detail{
			InputTokens: 1_000_000, OutputTokens: 500, TotalTokens: 1_000_500,
			TokenBreakdown: usage.NewSubsetTokenBreakdown(1_000_000, 0, 0, 500, 0, 1_000_500),
		},
	}
}

func TestBuiltinJevPrice(t *testing.T) {
	setup(t, config.ProxxPricingConfig{})
	for _, model := range []string{"jev-latest", "jev-1.14.0", "jev-preview"} {
		rec := jevRecord(model)
		ApplyCost(nil, &rec)
		near(t, rec.Detail.CostDetails.FinalCost, 0.042) // 1M input × $0.042, output free
		if rec.Detail.CostDetails.CostSource != SourceCatalog {
			t.Fatalf("%s source = %q", model, rec.Detail.CostDetails.CostSource)
		}
	}
	// Only the typesafe provider gets the built-in price.
	rec := jevRecord("jev-latest")
	rec.Provider = "openai"
	ApplyCost(nil, &rec)
	if rec.Detail.CostDetails.FinalCost != nil {
		t.Fatalf("jev priced under openai: %v", *rec.Detail.CostDetails.FinalCost)
	}
}

func TestConfigOverridesBuiltinJevPrice(t *testing.T) {
	setup(t, config.ProxxPricingConfig{Models: []config.ProxxModelPrice{{Model: "jev*", Provider: "typesafe", Input: f(1)}}})
	rec := jevRecord("jev-latest")
	ApplyCost(nil, &rec)
	near(t, rec.Detail.CostDetails.FinalCost, 1)
	if rec.Detail.CostDetails.CostSource != SourceConfig {
		t.Fatalf("source = %q", rec.Detail.CostDetails.CostSource)
	}
}
