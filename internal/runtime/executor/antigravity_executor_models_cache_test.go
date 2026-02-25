package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func resetAntigravityPrimaryModelsCacheForTest() {
	antigravityPrimaryModelsCache.mu.Lock()
	antigravityPrimaryModelsCache.models = nil
	antigravityPrimaryModelsCache.mu.Unlock()
}

func TestStoreAntigravityPrimaryModels_EmptyDoesNotOverwrite(t *testing.T) {
	resetAntigravityPrimaryModelsCacheForTest()
	t.Cleanup(resetAntigravityPrimaryModelsCacheForTest)

	seed := []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5"},
		{ID: "gemini-2.5-pro"},
	}
	if updated := storeAntigravityPrimaryModels(seed); !updated {
		t.Fatal("expected non-empty model list to update primary cache")
	}

	if updated := storeAntigravityPrimaryModels(nil); updated {
		t.Fatal("expected nil model list not to overwrite primary cache")
	}
	if updated := storeAntigravityPrimaryModels([]*registry.ModelInfo{}); updated {
		t.Fatal("expected empty model list not to overwrite primary cache")
	}

	got := loadAntigravityPrimaryModels()
	if len(got) != 2 {
		t.Fatalf("expected cached model count 2, got %d", len(got))
	}
	if got[0].ID != "claude-sonnet-4-5" || got[1].ID != "gemini-2.5-pro" {
		t.Fatalf("unexpected cached model ids: %q, %q", got[0].ID, got[1].ID)
	}
}

func TestLoadAntigravityPrimaryModels_ReturnsClone(t *testing.T) {
	resetAntigravityPrimaryModelsCacheForTest()
	t.Cleanup(resetAntigravityPrimaryModelsCacheForTest)

	if updated := storeAntigravityPrimaryModels([]*registry.ModelInfo{{
		ID:                         "gpt-5",
		DisplayName:                "GPT-5",
		SupportedGenerationMethods: []string{"generateContent"},
		SupportedParameters:        []string{"temperature"},
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high"},
		},
	}}); !updated {
		t.Fatal("expected model cache update")
	}

	got := loadAntigravityPrimaryModels()
	if len(got) != 1 {
		t.Fatalf("expected one cached model, got %d", len(got))
	}
	got[0].ID = "mutated-id"
	if len(got[0].SupportedGenerationMethods) > 0 {
		got[0].SupportedGenerationMethods[0] = "mutated-method"
	}
	if len(got[0].SupportedParameters) > 0 {
		got[0].SupportedParameters[0] = "mutated-parameter"
	}
	if got[0].Thinking != nil && len(got[0].Thinking.Levels) > 0 {
		got[0].Thinking.Levels[0] = "mutated-level"
	}

	again := loadAntigravityPrimaryModels()
	if len(again) != 1 {
		t.Fatalf("expected one cached model after mutation, got %d", len(again))
	}
	if again[0].ID != "gpt-5" {
		t.Fatalf("expected cached model id to remain %q, got %q", "gpt-5", again[0].ID)
	}
	if len(again[0].SupportedGenerationMethods) == 0 || again[0].SupportedGenerationMethods[0] != "generateContent" {
		t.Fatalf("expected cached generation methods to be unmutated, got %v", again[0].SupportedGenerationMethods)
	}
	if len(again[0].SupportedParameters) == 0 || again[0].SupportedParameters[0] != "temperature" {
		t.Fatalf("expected cached supported parameters to be unmutated, got %v", again[0].SupportedParameters)
	}
	if again[0].Thinking == nil || len(again[0].Thinking.Levels) == 0 || again[0].Thinking.Levels[0] != "high" {
		t.Fatalf("expected cached model thinking levels to be unmutated, got %v", again[0].Thinking)
	}
}
