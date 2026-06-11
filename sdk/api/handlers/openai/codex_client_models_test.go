package openai

import "testing"

func TestBuildCodexClientModelsAppliesContextLengthToTemplateModel(t *testing.T) {
	models := buildCodexClientModels([]map[string]any{{
		"id":             "gpt-5.4",
		"object":         "model",
		"owned_by":       "heybox",
		"context_length": 700000,
	}})

	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if got := intModelValue(models[0], "context_window"); got != 700000 {
		t.Fatalf("context_window = %d, want 700000", got)
	}
	if got := intModelValue(models[0], "max_context_window"); got != 700000 {
		t.Fatalf("max_context_window = %d, want 700000", got)
	}
	if _, ok := models[0]["apply_patch_tool_type"]; !ok {
		t.Fatal("expected template metadata to be preserved")
	}
}
