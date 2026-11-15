package pricing

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestLookupListingResolvesCompatAlias(t *testing.T) {
	c := setup(t, config.ProxxPricingConfig{})
	prev := aliasTargets.Load()
	t.Cleanup(func() { aliasTargets.Store(prev) })
	SetAliases(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "freeprov",
		Models: []config.OpenAICompatibilityModel{
			{Name: "gpt-4o-mini", Alias: "m-good"},
			{Name: "no-price-model", Alias: "m-bad"},
		},
	}}})
	st := currentState()
	m, ok := lookupListing(st, c, "openai-compatible-freeprov", "m-good")
	if !ok || m.Price.Input == nil || *m.Price.Input != 0.15 {
		t.Fatalf("alias m-good should price as gpt-4o-mini, got %+v ok=%v", m, ok)
	}
	if _, ok := lookupListing(st, c, "openai-compatible-freeprov", "m-bad"); ok {
		t.Fatal("alias to an unpriced model must stay unknown")
	}
}
