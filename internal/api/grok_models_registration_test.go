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
	foundModel := false
	for _, m := range models {
		if m.ID == modelID {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Fatalf("expected enriched models to include %s: %#v", modelID, models)
	}

	providers := registry.GetGlobalRegistry().GetModelProviders(modelID)
	if len(providers) != 1 || providers[0] != "grok" {
		t.Fatalf("expected discovered model to route via grok, got providers %v", providers)
	}

	clientModels := registry.GetGlobalRegistry().GetModelsForClient(authID)
	foundClientModel := false
	for _, m := range clientModels {
		if m.ID == modelID {
			foundClientModel = true
			break
		}
	}
	if !foundClientModel {
		t.Fatalf("expected discovered model registered for auth, got %#v", clientModels)
	}
}

func TestEnrichWithPerAuthGrokModelsKeepsStaticGrokModelsWhenLiveFetchOmitsThem(t *testing.T) {
	const authID = "grok-static-merge-registration-test"
	const liveModelID = "grok-4.3-live-test"
	const staticModelID = "grok-code-fast-1"

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
		ID:      liveModelID,
		Object:  "model",
		OwnedBy: "xai",
		Type:    "grok",
	}}}, false, seen)

	ids := make(map[string]struct{}, len(models))
	for _, m := range models {
		ids[m.ID] = struct{}{}
	}
	if _, ok := ids[liveModelID]; !ok {
		t.Fatalf("expected live model %s in enriched models", liveModelID)
	}
	if _, ok := ids[staticModelID]; !ok {
		t.Fatalf("expected static model %s to remain available", staticModelID)
	}

	providers := registry.GetGlobalRegistry().GetModelProviders(staticModelID)
	if len(providers) != 1 || providers[0] != "grok" {
		t.Fatalf("expected static model to route via grok, got providers %v", providers)
	}
}
