package registry

import (
	"fmt"
	"testing"
)

func TestGetAvailableModelsByProviderMergesSupportsWebSearch(t *testing.T) {
	const (
		provider = "antigravity"
		modelID  = "gemini-2.5-flash"
	)

	for iter := 0; iter < 50; iter++ {
		reg := newTestModelRegistry()
		for i := 0; i < 4; i++ {
			reg.RegisterClient(fmt.Sprintf("client-no-ws-%d-%d", iter, i), provider, []*ModelInfo{
				{
					ID:                modelID,
					Type:              provider,
					SupportsWebSearch: false,
				},
			})
		}
		reg.RegisterClient(fmt.Sprintf("client-with-ws-%d", iter), provider, []*ModelInfo{
			{
				ID:                modelID,
				Type:              provider,
				SupportsWebSearch: true,
			},
		})

		models := reg.GetAvailableModelsByProvider(provider)
		if len(models) != 1 {
			t.Fatalf("iter %d: expected 1 model, got %d", iter, len(models))
		}
		if models[0].ID != modelID {
			t.Fatalf("iter %d: expected model ID %q, got %q", iter, modelID, models[0].ID)
		}
		if !models[0].SupportsWebSearch {
			t.Fatalf("iter %d: expected SupportsWebSearch=true when any client of provider %q supports web search, got false", iter, provider)
		}
	}
}

func TestGetAvailableModelsByProviderDoesNotLeakSupportsWebSearchAcrossProviders(t *testing.T) {
	const modelID = "gemini-2.5-flash"

	reg := newTestModelRegistry()
	reg.RegisterClient("gemini-ws", "gemini", []*ModelInfo{
		{
			ID:                modelID,
			Type:              "gemini",
			SupportsWebSearch: true,
		},
	})
	reg.RegisterClient("antigravity-no-ws-1", "antigravity", []*ModelInfo{
		{
			ID:                modelID,
			Type:              "antigravity",
			SupportsWebSearch: false,
		},
	})
	reg.RegisterClient("antigravity-no-ws-2", "antigravity", []*ModelInfo{
		{
			ID:                modelID,
			Type:              "antigravity",
			SupportsWebSearch: false,
		},
	})

	agModels := reg.GetAvailableModelsByProvider("antigravity")
	if len(agModels) != 1 {
		t.Fatalf("expected 1 antigravity model, got %d", len(agModels))
	}
	if agModels[0].SupportsWebSearch {
		t.Fatalf("expected antigravity SupportsWebSearch=false when only gemini supports web search, got true")
	}

	gemModels := reg.GetAvailableModelsByProvider("gemini")
	if len(gemModels) != 1 {
		t.Fatalf("expected 1 gemini model, got %d", len(gemModels))
	}
	if !gemModels[0].SupportsWebSearch {
		t.Fatalf("expected gemini SupportsWebSearch=true, got false")
	}
}
