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
		// Register multiple clients for the same provider and model where only one
		// client advertises SupportsWebSearch: true. Because GetAvailableModelsByProvider
		// iterates over r.clientProviders (a Go map), latching entry.info from the
		// first visited client without merging SupportsWebSearch causes non-deterministic
		// false results across iterations.
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
