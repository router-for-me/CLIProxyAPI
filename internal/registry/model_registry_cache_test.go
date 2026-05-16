package registry

import "testing"

func TestGetAvailableModelsReturnsClonedSnapshots(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team-a", DisplayName: "Model One"}})

	first := r.GetAvailableModels("openai")
	if len(first) != 1 {
		t.Fatalf("expected 1 model, got %d", len(first))
	}
	first[0]["id"] = "mutated"
	first[0]["display_name"] = "Mutated"

	second := r.GetAvailableModels("openai")
	if got := second[0]["id"]; got != "m1" {
		t.Fatalf("expected cached snapshot to stay isolated, got id %v", got)
	}
	if got := second[0]["display_name"]; got != "Model One" {
		t.Fatalf("expected cached snapshot to stay isolated, got display_name %v", got)
	}
}

func TestGetAvailableModelsOpenAIPreservesMetadata(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{
		ID:                  "m1",
		Object:              "model",
		Created:             1776902400,
		OwnedBy:             "openai",
		Type:                "openai",
		DisplayName:         "Model One",
		Version:             "m1",
		Description:         "Model with metadata.",
		ContextLength:       272000,
		MaxCompletionTokens: 128000,
		SupportedParameters: []string{"tools"},
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	model := models[0]

	want := map[string]any{
		"id":                    "m1",
		"object":                "model",
		"created":               int64(1776902400),
		"owned_by":              "openai",
		"type":                  "openai",
		"display_name":          "Model One",
		"version":               "m1",
		"description":           "Model with metadata.",
		"context_length":        272000,
		"max_completion_tokens": 128000,
	}
	for key, expected := range want {
		if got := model[key]; got != expected {
			t.Fatalf("expected %s=%#v, got %#v", key, expected, got)
		}
	}

	params, ok := model["supported_parameters"].([]string)
	if !ok || len(params) != 1 || params[0] != "tools" {
		t.Fatalf("expected supported_parameters [tools], got %#v", model["supported_parameters"])
	}
}

func TestGetAvailableModelsInvalidatesCacheOnRegistryChanges(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team-a", DisplayName: "Model One"}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if got := models[0]["display_name"]; got != "Model One" {
		t.Fatalf("expected initial display_name Model One, got %v", got)
	}

	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1", OwnedBy: "team-a", DisplayName: "Model One Updated"}})
	models = r.GetAvailableModels("openai")
	if got := models[0]["display_name"]; got != "Model One Updated" {
		t.Fatalf("expected updated display_name after cache invalidation, got %v", got)
	}

	r.SuspendClientModel("client-1", "m1", "manual")
	models = r.GetAvailableModels("openai")
	if len(models) != 0 {
		t.Fatalf("expected no available models after suspension, got %d", len(models))
	}

	r.ResumeClientModel("client-1", "m1")
	models = r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected model to reappear after resume, got %d", len(models))
	}
}
