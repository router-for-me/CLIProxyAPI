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

// Usage records and the registry carry compat providers as "openai-compatible-<name>";
// a config entry restricted to the bare name must still match them.
func TestConfigProviderMatchesRuntimeCompatKey(t *testing.T) {
	setup(t, config.ProxxPricingConfig{Models: []config.ProxxModelPrice{
		{Model: "or-model", Provider: "openrouter", Input: f(1), Output: f(1), Override: true},
	}})
	m, ok := Lookup("openai-compatible-openrouter", "or-model")
	if !ok || m.Source != SourceConfig || m.Price.Input == nil || *m.Price.Input != 1 {
		t.Fatalf("provider-restricted entry must match runtime compat key, got %+v ok=%v", m, ok)
	}
	if _, ok := Lookup("openai-compatible-groq", "or-model"); ok {
		t.Fatal("entry restricted to openrouter must not match groq")
	}
}
