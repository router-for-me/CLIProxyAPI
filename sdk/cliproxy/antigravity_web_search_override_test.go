package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// authWithoutAccessToken yields an auth that makes the upstream capability fetch
// return empty without issuing a request, isolating the override behavior from
// the network.
func authWithoutAccessToken() *coreauth.Auth {
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

// The upstream fetch is the only capability source today, so an unreachable or
// silent catalog must not drop a configured override.
func TestAntigravityModelCapabilityHintsMergesOverridesWithoutUpstreamHints(t *testing.T) {
	service := serviceWithWebSearchModels("gemini-3.7-flash-high")

	hints := service.antigravityModelCapabilityHints(context.Background(), authWithoutAccessToken())

	if _, ok := hints.WebSearchModelIDs["gemini-3.7-flash-high"]; !ok {
		t.Fatalf("hints = %v, want configured override present", hints.WebSearchModelIDs)
	}
}

func TestAntigravityModelCapabilityHintsWithoutOverridesStaysEmpty(t *testing.T) {
	service := &Service{cfg: &config.Config{}}

	hints := service.antigravityModelCapabilityHints(context.Background(), authWithoutAccessToken())

	if len(hints.WebSearchModelIDs) != 0 {
		t.Fatalf("hints = %v, want empty when nothing is configured or fetched", hints.WebSearchModelIDs)
	}
}

// The override is a union with the upstream list, never a replacement.
func TestApplyAntigravityFetchedModelCapabilitiesUnionsFetchedAndOverridden(t *testing.T) {
	service := serviceWithWebSearchModels("gemini-override-only")
	hints := service.antigravityModelCapabilityHints(context.Background(), authWithoutAccessToken())
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

// End to end: an overridden model must satisfy the gate the Claude web_search
// translator consults, which is what makes the tool emit a native googleSearch
// request instead of degrading into a plain chat turn.
func TestOverriddenModelResolvesThroughAntigravityWebSearchModelFor(t *testing.T) {
	const (
		clientID     = "test-antigravity-web-search-override"
		overriddenID = "gemini-override-e2e"
		uncoveredID  = "gemini-no-override-e2e"
	)

	service := serviceWithWebSearchModels(overriddenID)
	hints := service.antigravityModelCapabilityHints(context.Background(), authWithoutAccessToken())
	models := applyAntigravityFetchedModelCapabilities([]*internalregistry.ModelInfo{
		{ID: overriddenID},
		{ID: uncoveredID},
	}, hints)

	registry := GlobalModelRegistry()
	registry.RegisterClient(clientID, "antigravity", models)
	t.Cleanup(func() { registry.UnregisterClient(clientID) })

	if got := internalregistry.AntigravityWebSearchModelFor(overriddenID); got != overriddenID {
		t.Fatalf("AntigravityWebSearchModelFor(%q) = %q, want %q", overriddenID, got, overriddenID)
	}
	// The thinking-suffix form routes to the same capability decision.
	if got := internalregistry.AntigravityWebSearchModelFor(overriddenID + "(high)"); got != overriddenID {
		t.Fatalf("AntigravityWebSearchModelFor(%q) = %q, want %q", overriddenID+"(high)", got, overriddenID)
	}
	if got := internalregistry.AntigravityWebSearchModelFor(uncoveredID); got != "" {
		t.Fatalf("AntigravityWebSearchModelFor(%q) = %q, want empty", uncoveredID, got)
	}
}
