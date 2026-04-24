package registry

import "testing"

func TestGetCodexModelsIncludeGPT55AcrossPlans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		get  func() []*ModelInfo
	}{
		{name: "free", get: GetCodexFreeModels},
		{name: "team", get: GetCodexTeamModels},
		{name: "plus", get: GetCodexPlusModels},
		{name: "pro", get: GetCodexProModels},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			models := tt.get()
			if !containsModelID(models, "gpt-5.5") {
				t.Fatalf("expected gpt-5.5 in %s codex model list", tt.name)
			}
		})
	}
}

func containsModelID(models []*ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && model.ID == id {
			return true
		}
	}
	return false
}
