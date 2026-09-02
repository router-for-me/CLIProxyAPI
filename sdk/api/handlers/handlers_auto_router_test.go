package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// stubAutoRouterResolver returns a fixed Auto Router for the model id it was
// configured with, mimicking the store resolver contract for tests.
type stubAutoRouterResolver struct {
	router *store.AutoRouter
}

func (r stubAutoRouterResolver) AutoRouterForModel(_ context.Context, modelID string) *store.AutoRouter {
	if r.router != nil && r.router.ModelID == modelID {
		return r.router
	}
	return nil
}

// TestResolveAutoRouterCapturesTierAndRouterID verifies that when a request
// matches an Auto Router, resolveAutoRouterModel populates the resolved tier
// and the router's client-facing id, which the caller threads into the usage
// context for later attribution by the UsageReporter.
func TestResolveAutoRouterCapturesTierAndRouterID(t *testing.T) {
	const routerID = "router:smart"
	router := &store.AutoRouter{
		ID:      "router-1",
		ModelID: routerID,
		Name:    "Smart Router",
		Enabled: true,
		Mappings: []store.TierMapping{
			{Tier: "simple", Model: "claude-sonnet-4-5"},
			{Tier: "medium", Model: "claude-sonnet-4-5"},
			{Tier: "complex", Model: "claude-opus-4-5"},
			{Tier: "reasoning", Model: "claude-opus-4-5"},
		},
	}
	h := &BaseAPIHandler{AutoRouterResolver: stubAutoRouterResolver{router: router}}

	// A trivial greeting scores into the simple tier, but we keep the assertion
	// to the round-trip contract (non-empty tier/routerID + routerID matches the
	// configured ModelID) so the test does not couple to scorer internals.
	res := h.resolveAutoRouterModel(context.Background(), "openai", routerID,
		[]byte(`{"model":"`+routerID+`","messages":[{"role":"user","content":"hi"}]}`))
	if !res.matched {
		t.Fatalf("expected auto router to resolve for model %q, got unmatched", routerID)
	}
	if res.tier == "" {
		t.Error("expected a non-empty resolved tier")
	}
	if res.routerID != routerID {
		t.Errorf("resolved routerID = %q, want %q", res.routerID, routerID)
	}
	if res.targetModel == "" {
		t.Error("expected a non-empty target model")
	}

	// The matched tier must correspond to one of the configured tier mappings.
	if _, ok := map[string]bool{"simple": true, "medium": true, "complex": true, "reasoning": true}[res.tier]; !ok {
		t.Errorf("resolved tier %q is not one of the configured tiers", res.tier)
	}
}

// TestResolveAutoRouterUnmatchedLeavesTierEmpty verifies that non-router model
// ids (and disabled routers) leave tier/routerID empty so no usage attribution
// is applied for ordinary routing.
func TestResolveAutoRouterUnmatchedLeavesTierEmpty(t *testing.T) {
	router := &store.AutoRouter{
		ID:      "router-1",
		ModelID: "router:smart",
		Name:    "Smart Router",
		Enabled: true,
		Mappings: []store.TierMapping{
			{Tier: "simple", Model: "claude-sonnet-4-5"},
		},
	}
	h := &BaseAPIHandler{AutoRouterResolver: stubAutoRouterResolver{router: router}}

	// Requested model is not an Auto Router id.
	if res := h.resolveAutoRouterModel(context.Background(), "openai", "some-regular-model",
		[]byte(`{"model":"some-regular-model","messages":[{"role":"user","content":"hi"}]}`)); res.matched {
		t.Fatalf("expected unmatched for a non-router model, got %+v", res)
	}

	// Disabled router must not match.
	hDisabled := &BaseAPIHandler{AutoRouterResolver: stubAutoRouterResolver{router: &store.AutoRouter{
		ID:      "router-1",
		ModelID: "router:smart",
		Enabled: false,
		Mappings: []store.TierMapping{
			{Tier: "simple", Model: "claude-sonnet-4-5"},
		},
	}}}
	if res := hDisabled.resolveAutoRouterModel(context.Background(), "openai", "router:smart",
		[]byte(`{"model":"router:smart","messages":[{"role":"user","content":"hi"}]}`)); res.matched {
		t.Fatalf("expected unmatched for a disabled router, got %+v", res)
	}
}

// TestApplyAutoRouterRouteEnforcesPinEmptyIntersection verifies that when the
// tier pin (route.Providers) shares zero providers with the computed execution
// providers, the route is enforced strictly: no providers are returned and an
// explicit error names the model and the pinned providers. The previous
// behaviour silently fell back to the full computed providers list, which let
// requests reach providers that were never pinned in the tier mapping.
func TestApplyAutoRouterRouteEnforcesPinEmptyIntersection(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "claude-opus-4-5",
		Providers: []string{"anthropic"},
	}
	computed := []string{"openai", "azure"}
	got, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg == nil {
		t.Fatalf("expected an explicit error when pin intersection is empty, got providers %v (pin=%v)", got, route.Providers)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty providers when pin intersection is empty, got %v (pin=%v)", got, route.Providers)
	}
}

// TestApplyAutoRouterRouteEnforcesPinPartialIntersection verifies that when
// some computed providers match the pin, only those providers are kept and the
// rest are filtered out — i.e. the pin is an authoritative override, not a
// hint.
func TestApplyAutoRouterRouteEnforcesPinPartialIntersection(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "claude-opus-4-5",
		Providers: []string{"anthropic"},
	}
	computed := []string{"openai", "anthropic", "azure"}
	got, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg != nil {
		t.Fatalf("unexpected error for a non-empty intersection: %v", errMsg.Error)
	}
	if len(got) != 1 || got[0] != "anthropic" {
		t.Fatalf("expected only pinned provider, got %v (pin=%v)", got, route.Providers)
	}
}

// TestApplyAutoRouterRouteNoPinReturnsComputed verifies the unchanged fallback:
// when the route has no pinned providers at all, the computed providers are
// passed through unchanged (the target model's default providers apply).
func TestApplyAutoRouterRouteNoPinReturnsComputed(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{Model: "claude-opus-4-5"}
	computed := []string{"openai", "azure"}
	got, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg != nil {
		t.Fatalf("unexpected error when no pin is set: %v", errMsg.Error)
	}
	if len(got) != 2 {
		t.Fatalf("expected computed providers to pass through, got %v", got)
	}
}

// TestApplyAutoRouterRouteOpenAIProviderPoolPinMatchesEntry pins down the
// routing-remediation fix: a tier pinning the OpenAI-compat pool at the
// provider level must match a registry entry that exposes only the
// per-entry compound key, mirroring what the dashboard can offer for the
// pool while the registry registers models under entry identities.
func TestApplyAutoRouterRouteOpenAIProviderPoolPinMatchesEntry(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "glm-5.3",
		Providers: []string{"openai-compatible-openlimits"},
	}
	computed := []string{"openai-compatible-openlimits:key-17"}
	got, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg != nil {
		t.Fatalf("unexpected error for provider-pool pin: %+v", errMsg)
	}
	if len(got) != 1 || got[0] != "openai-compatible-openlimits:key-17" {
		t.Fatalf("expected pool pin to retain entry provider, got %v", got)
	}
}

func TestApplyAutoRouterRouteOpenAIEntryPinStaysExact(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "glm-5.3",
		Providers: []string{"openai-compatible-openlimits:key-18"},
	}
	computed := []string{"openai-compatible-openlimits:key-17"}
	got, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg == nil {
		t.Fatalf("expected exact entry pin mismatch to return an error, got providers %v", got)
	}
}

func TestApplyAutoRouterRouteOpenAICompatColonPoolPinMatchesEntry(t *testing.T) {
	h := &BaseAPIHandler{}
	route := &autorouter.Resolved{
		Model:     "glm-5.3",
		Providers: []string{"openai-compatible-foo:bar"},
	}
	computed := []string{"openai-compatible-foo:bar:key-17"}
	got, errMsg := h.applyAutoRouterRoute(context.Background(), computed, route)
	if errMsg != nil {
		t.Fatalf("unexpected error for colon pool pin: %+v", errMsg)
	}
	if len(got) != 1 || got[0] != "openai-compatible-foo:bar:key-17" {
		t.Fatalf("expected colon pool pin to retain entry provider, got %v", got)
	}
}
