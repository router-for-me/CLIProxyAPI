package registry

import "testing"

func TestGetCodeBuddyModelsIncludesVerifiedBuiltIns(t *testing.T) {
	models := GetCodeBuddyModels()
	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, exists := byID[model.ID]; exists {
			t.Fatalf("duplicate CodeBuddy model ID %q", model.ID)
		}
		byID[model.ID] = model
	}

	for _, id := range []string{"hy3-preview", "minimax-m2.5", "kimi-k2.6", "kimi-k2-thinking"} {
		if byID[id] == nil {
			t.Fatalf("expected CodeBuddy model %q to be registered", id)
		}
	}

	if byID["hy3-preview"].Thinking == nil {
		t.Fatal("expected hy3-preview to advertise thinking support")
	}
}
