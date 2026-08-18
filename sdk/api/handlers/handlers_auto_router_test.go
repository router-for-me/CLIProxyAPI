package handlers

import (
	"context"
	"testing"

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
