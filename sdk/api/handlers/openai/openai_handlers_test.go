package openai

import "testing"

func TestFilterOpenAIModelsResponseIncludesContextLength(t *testing.T) {
	models := filterOpenAIModelsResponse([]map[string]any{{
		"id":             "gpt-5.4",
		"object":         "model",
		"created":        int64(123),
		"owned_by":       "heybox",
		"context_length": 700000,
		"display_name":   "GPT-5.4",
	}})

	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if got := intModelValue(models[0], "context_length"); got != 700000 {
		t.Fatalf("context_length = %d, want 700000", got)
	}
	if _, ok := models[0]["display_name"]; ok {
		t.Fatal("expected display_name to remain filtered")
	}
}
