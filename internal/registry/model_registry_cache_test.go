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

func TestGetAvailableModelsClaudeIncludesTokenLimits(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "Claude", []*ModelInfo{
		{ID: "claude-sonnet-4-6", OwnedBy: "anthropic", Type: "claude", Created: 1771372800, ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "claude-no-limits", OwnedBy: "anthropic", Type: "claude"},
	})

	models := r.GetAvailableModels("claude")
	byID := make(map[string]map[string]any, len(models))
	for _, m := range models {
		id, _ := m["id"].(string)
		byID[id] = m
	}

	withLimits, ok := byID["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("expected claude-sonnet-4-6 in available models, got %v", byID)
	}
	if got := withLimits["max_input_tokens"]; got != 200000 {
		t.Fatalf("expected max_input_tokens 200000, got %v", got)
	}
	if got := withLimits["max_tokens"]; got != 64000 {
		t.Fatalf("expected max_tokens 64000, got %v", got)
	}
	if got := withLimits["created_at"]; got != "2026-02-18T00:00:00Z" {
		t.Fatalf("expected created_at as RFC 3339 string, got %v", got)
	}

	withDefaults, ok := byID["claude-no-limits"]
	if !ok {
		t.Fatalf("expected claude-no-limits in available models, got %v", byID)
	}
	if got := withDefaults["max_input_tokens"]; got != defaultClaudeMaxInputTokens {
		t.Fatalf("expected fallback max_input_tokens %d, got %v", defaultClaudeMaxInputTokens, got)
	}
	if got := withDefaults["max_tokens"]; got != defaultClaudeMaxOutputTokens {
		t.Fatalf("expected fallback max_tokens %d, got %v", defaultClaudeMaxOutputTokens, got)
	}
	if got := withDefaults["display_name"]; got != "claude-no-limits" {
		t.Fatalf("expected display_name to fall back to id, got %v", got)
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
