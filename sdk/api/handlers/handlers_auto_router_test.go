package handlers

import (
	"context"
	"net/http"
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
// and the router's PK id, which the caller threads into the usage context for
// later attribution by the UsageReporter. The PK (not the requestable model id)
// is the attribution key: usage_events.router_id must match the identifier the
// management endpoints (decisions/simulate/replay) address the router by.
func TestResolveAutoRouterCapturesTierAndRouterID(t *testing.T) {
	const modelID = "router:smart"
	const pkID = "router-1"
	router := &store.AutoRouter{
		ID:      pkID,
		ModelID: modelID,
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
	// to the round-trip contract (non-empty tier/routerID) so the test does not
	// couple to scorer internals. routerID must be the PK id, NOT ModelID.
	res := h.resolveAutoRouterModel(context.Background(), "openai", modelID,
		[]byte(`{"model":"`+modelID+`","messages":[{"role":"user","content":"hi"}]}`))
	if !res.matched {
		t.Fatalf("expected auto router to resolve for model %q, got unmatched", modelID)
	}
	if res.tier == "" {
		t.Error("expected a non-empty resolved tier")
	}
	if res.routerID != pkID {
		t.Errorf("resolved routerID = %q, want PK %q (not model id)", res.routerID, pkID)
	}
	if res.routerID == modelID {
		t.Error("routerID must not be the requestable model id (attribution mismatch)")
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

// TestResolveAutoRouterNoMappingFailsExplicitly verifies that an enabled router
// whose tier mapping cannot resolve reports resolveFailed with tier/routerID
// populated, instead of silently falling through to the synthetic provider.
func TestResolveAutoRouterNoMappingFailsExplicitly(t *testing.T) {
	router := &store.AutoRouter{
		ID:      "router-2",
		ModelID: "router:broken",
		Name:    "Broken Router",
		Enabled: true,
		// No mappings at all -> Resolve must fail for every tier.
	}
	h := &BaseAPIHandler{AutoRouterResolver: stubAutoRouterResolver{router: router}}
	res := h.resolveAutoRouterModel(context.Background(), "openai", "router:broken",
		[]byte(`{"model":"router:broken","messages":[{"role":"user","content":"hi"}]}`))
	if res.matched {
		t.Fatal("unresolvable router must not be reported as matched")
	}
	if !res.resolveFailed {
		t.Fatal("expected resolveFailed=true for a router with no resolvable mapping")
	}
	if res.routerID != "router-2" {
		t.Fatalf("routerID = %q, want router-2", res.routerID)
	}
	if res.tier == "" {
		t.Fatal("expected the scored tier to be populated for diagnostics")
	}
	// The ready-made error must be explicit and diagnosable.
	errMsg := res.resolveFailureError()
	if errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("resolveFailureError() = %+v, want 503", errMsg)
	}
}

// TestResolveAutoRouterUnmatchedNotFailed verifies non-router models never set
// resolveFailed (the flag is only for router matches whose mapping fails).
func TestResolveAutoRouterUnmatchedNotFailed(t *testing.T) {
	h := &BaseAPIHandler{}
	res := h.resolveAutoRouterModel(context.Background(), "openai", "gpt-4o",
		[]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if res.matched || res.resolveFailed {
		t.Fatalf("non-router model must be neither matched nor resolveFailed, got %+v", res)
	}
}
