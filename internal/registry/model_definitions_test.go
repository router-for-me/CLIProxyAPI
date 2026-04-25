package registry

import "testing"

func TestCodexStaticModelsIncludeGPT55WithExpectedContextLength(t *testing.T) {
	tests := []struct {
		name   string
		models []*ModelInfo
	}{
		{name: "team", models: GetCodexTeamModels()},
		{name: "plus", models: GetCodexPlusModels()},
		{name: "pro", models: GetCodexProModels()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := findModelInfo(tt.models, "gpt-5.5")
			if info == nil {
				t.Fatal("gpt-5.5 not found")
			}
			if info.ContextLength != 1050000 {
				t.Fatalf("context length = %d, want 1050000", info.ContextLength)
			}
		})
	}

	info := LookupStaticModelInfo("gpt-5.5")
	if info == nil {
		t.Fatal("LookupStaticModelInfo did not find gpt-5.5")
	}
	if info.ContextLength != 1050000 {
		t.Fatalf("lookup context length = %d, want 1050000", info.ContextLength)
	}
}

func TestCodexFreeStaticModelsDoNotIncludeGPT55(t *testing.T) {
	if info := findModelInfo(GetCodexFreeModels(), "gpt-5.5"); info != nil {
		t.Fatal("gpt-5.5 should not be available for codex-free")
	}
}

func findModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}
