package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func antigravityOverrideAuthWithoutAccessToken() *coreauth.Auth {
	return &coreauth.Auth{ID: "auth-antigravity-override", Provider: "antigravity"}
}

func serviceWithWebSearchModels(models ...string) *Service {
	cfg := &config.Config{}
	cfg.Antigravity.WebSearchModels = models
	return &Service{cfg: cfg}
}

func TestAntigravityWebSearchModelOverridesNormalizesEntries(t *testing.T) {
	service := serviceWithWebSearchModels("Gemini-3.7-Flash-High", "  gemini-3-flash  ", "", "   ")
	overrides := service.antigravityWebSearchModelOverrides()
	if len(overrides) != 2 {
		t.Fatalf("overrides = %v, want 2 normalized entries", overrides)
	}
	for _, want := range []string{"gemini-3.7-flash-high", "gemini-3-flash"} {
		if _, ok := overrides[want]; !ok {
			t.Fatalf("overrides missing %q: %v", want, overrides)
		}
	}
}

func TestAntigravityWebSearchModelOverridesEmptyWithoutConfig(t *testing.T) {
	if got := (&Service{cfg: &config.Config{}}).antigravityWebSearchModelOverrides(); got != nil {
		t.Fatalf("unconfigured overrides = %v, want nil", got)
	}
	if got := (&Service{}).antigravityWebSearchModelOverrides(); got != nil {
		t.Fatalf("nil-config overrides = %v, want nil", got)
	}
	if got := (*Service)(nil).antigravityWebSearchModelOverrides(); got != nil {
		t.Fatalf("nil-service overrides = %v, want nil", got)
	}
}

func TestAntigravityModelCapabilityHintsMergesOverridesWithoutUpstreamHints(t *testing.T) {
	service := serviceWithWebSearchModels("gemini-3.7-flash-high")
	hints := service.antigravityModelCapabilityHintsForAuth(context.Background(), antigravityOverrideAuthWithoutAccessToken())
	if _, ok := hints.WebSearchModelIDs["gemini-3.7-flash-high"]; !ok {
		t.Fatalf("hints = %v, want configured override present", hints.WebSearchModelIDs)
	}
}

func TestApplyAntigravityFetchedModelCapabilitiesUnionsFetchedAndOverridden(t *testing.T) {
	service := serviceWithWebSearchModels("gemini-override-only")
	hints := service.antigravityModelCapabilityHintsForAuth(context.Background(), antigravityOverrideAuthWithoutAccessToken())
	hints.WebSearchModelIDs["gemini-fetched-only"] = struct{}{}

	models := applyAntigravityFetchedModelCapabilities([]*internalregistry.ModelInfo{
		{ID: "gemini-override-only"},
		{ID: "gemini-fetched-only"},
		{ID: "gemini-untouched"},
	}, hints)
	got := make(map[string]bool, len(models))
	for _, model := range models {
		got[model.ID] = model.SupportsWebSearch
	}
	for _, id := range []string{"gemini-override-only", "gemini-fetched-only"} {
		if !got[id] {
			t.Fatalf("%s SupportsWebSearch = false, want true: %v", id, got)
		}
	}
	if got["gemini-untouched"] {
		t.Fatalf("gemini-untouched SupportsWebSearch = true, want false: %v", got)
	}
}
