package api

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeGrokModelFetcher struct {
	models []*registry.ModelInfo
	err    error
}

func (f fakeGrokModelFetcher) FetchAvailableModels(context.Context, *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	return f.models, f.err
}

func TestEnrichWithPerAuthGrokModelsRegistersDiscoveredModelsForRouting(t *testing.T) {
	const authID = "grok-live-model-registration-test"
	const modelID = "grok-4.3-live-test"

	registry.UnregisterAuth(authID)
	t.Cleanup(func() { registry.UnregisterAuth(authID) })

	manager := cliproxyauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID:       authID,
		Provider: "grok",
		Status:   cliproxyauth.StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	seen := map[string]struct{}{}
	models := enrichWithPerAuthGrokModels(context.Background(), manager, fakeGrokModelFetcher{models: []*registry.ModelInfo{{
		ID:      modelID,
		Object:  "model",
		OwnedBy: "xai",
		Type:    "grok",
	}}}, false, seen)
	if len(models) != 1 || models[0].ID != modelID {
		t.Fatalf("unexpected enriched models: %#v", models)
	}

	providers := registry.GetGlobalRegistry().GetModelProviders(modelID)
	if len(providers) != 1 || providers[0] != "grok" {
		t.Fatalf("expected discovered model to route via grok, got providers %v", providers)
	}

	clientModels := registry.GetGlobalRegistry().GetModelsForClient(authID)
	if len(clientModels) != 1 || clientModels[0].ID != modelID {
		t.Fatalf("expected discovered model registered for auth, got %#v", clientModels)
	}
}
