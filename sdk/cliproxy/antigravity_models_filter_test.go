package cliproxy

import "testing"

func TestFilterAntigravityModelsNilCatalogPreservesModels(t *testing.T) {
	models := []*ModelInfo{
		{ID: "gemini-3-flash"},
		{ID: "gemini-3.1-flash-lite"},
	}

	filtered := filterAntigravityModels(models, antigravityModelCapabilityHints{})
	if len(filtered) != len(models) {
		t.Fatalf("nil catalog filtered %d of %d models", len(models)-len(filtered), len(models))
	}

	for i := range models {
		if filtered[i] != models[i] {
			t.Fatalf("model %d changed when catalog was unknown", i)
		}
	}
}

func TestFilterAntigravityModelsEmptyCatalogFiltersModels(t *testing.T) {
	models := []*ModelInfo{{ID: "gemini-3-flash"}}

	filtered := filterAntigravityModels(models, antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{},
	})
	if len(filtered) != 0 {
		t.Fatalf("empty known catalog preserved %d models", len(filtered))
	}
}
