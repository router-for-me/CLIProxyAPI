package registry

import "testing"

func TestGrokStaticModelsExposeToolCapabilities(t *testing.T) {
	for _, id := range []string{"grok-code-fast-1", "grok-4-fast"} {
		t.Run(id, func(t *testing.T) {
			model := findGrokModelInfo(GetGrokModels(), id)
			if model == nil {
				t.Fatalf("expected Grok static model %q", id)
			}
			if model.Type != "grok" || model.OwnedBy != "xai" {
				t.Fatalf("unexpected Grok model identity: type=%q owned_by=%q", model.Type, model.OwnedBy)
			}
			if model.ContextLength != 131072 || model.MaxCompletionTokens != 16384 {
				t.Fatalf("unexpected Grok limits: context=%d max_completion=%d", model.ContextLength, model.MaxCompletionTokens)
			}
			if len(model.SupportedParameters) != 1 || model.SupportedParameters[0] != "tools" {
				t.Fatalf("unexpected Grok supported parameters: %v", model.SupportedParameters)
			}
		})
	}
}

func findGrokModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}
