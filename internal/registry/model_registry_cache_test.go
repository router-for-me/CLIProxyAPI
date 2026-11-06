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
	if got := withDefaults["max_input_tokens"]; got != DefaultClaudeMaxInputTokens {
		t.Fatalf("expected fallback max_input_tokens %d, got %v", DefaultClaudeMaxInputTokens, got)
	}
	if got := withDefaults["max_tokens"]; got != DefaultClaudeMaxOutputTokens {
		t.Fatalf("expected fallback max_tokens %d, got %v", DefaultClaudeMaxOutputTokens, got)
	}
	if got := withDefaults["display_name"]; got != "claude-no-limits" {
		t.Fatalf("expected display_name to fall back to id, got %v", got)
	}
	if got := withDefaults["type"]; got != "model" {
		t.Fatalf("expected type to default to model, got %v", got)
	}
}

func TestGetAvailableModelsGeminiIncludesTokenLimits(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{
		{
			ID:                  "gemini-3.8-flash",
			Name:                "models/gemini-3.8-flash",
			DisplayName:         "Gemini 3.8 Flash",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
		},
		{
			ID:               "gemini-2.5-pro",
			Name:             "models/gemini-2.5-pro",
			DisplayName:      "Gemini 2.5 Pro",
			InputTokenLimit:  1048576,
			OutputTokenLimit: 65536,
		},
		{
			ID:   "gemini-no-limits",
			Name: "models/gemini-no-limits",
		},
	})

	models := r.GetAvailableModels("gemini")
	byName := make(map[string]map[string]any, len(models))
	for _, m := range models {
		name, _ := m["name"].(string)
		byName[name] = m
	}

	fromContext, ok := byName["models/gemini-3.8-flash"]
	if !ok {
		t.Fatalf("expected models/gemini-3.8-flash in available models, got %v", byName)
	}
	if got := fromContext["inputTokenLimit"]; got != 1048576 {
		t.Fatalf("gemini-3.8-flash inputTokenLimit = %v, want 1048576", got)
	}
	if got := fromContext["outputTokenLimit"]; got != 65536 {
		t.Fatalf("gemini-3.8-flash outputTokenLimit = %v, want 65536", got)
	}

	fromExplicit, ok := byName["models/gemini-2.5-pro"]
	if !ok {
		t.Fatalf("expected models/gemini-2.5-pro in available models, got %v", byName)
	}
	if got := fromExplicit["inputTokenLimit"]; got != 1048576 {
		t.Fatalf("gemini-2.5-pro inputTokenLimit = %v, want 1048576", got)
	}
	if got := fromExplicit["outputTokenLimit"]; got != 65536 {
		t.Fatalf("gemini-2.5-pro outputTokenLimit = %v, want 65536", got)
	}

	noLimits, ok := byName["models/gemini-no-limits"]
	if !ok {
		t.Fatalf("expected models/gemini-no-limits in available models, got %v", byName)
	}
	if _, exists := noLimits["inputTokenLimit"]; exists {
		t.Fatalf("expected inputTokenLimit to be omitted when unknown, got %v", noLimits)
	}
	if _, exists := noLimits["outputTokenLimit"]; exists {
		t.Fatalf("expected outputTokenLimit to be omitted when unknown, got %v", noLimits)
	}
}

func TestGetAvailableModelsOpenAIFallsBackToProviderTokenLimits(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{
		{ID: "explicit", ContextLength: 372000, MaxCompletionTokens: 128000, InputTokenLimit: 500000, OutputTokenLimit: 64000},
		{ID: "fallback", InputTokenLimit: 1048576, OutputTokenLimit: 65536},
		{ID: "without-limits"},
	})

	models := r.GetAvailableModels("openai")
	byID := make(map[string]map[string]any, len(models))
	for _, model := range models {
		byID[model["id"].(string)] = model
	}

	if got := byID["explicit"]["context_length"]; got != 372000 {
		t.Fatalf("explicit context_length = %v, want 372000", got)
	}
	if got := byID["explicit"]["max_completion_tokens"]; got != 128000 {
		t.Fatalf("explicit max_completion_tokens = %v, want 128000", got)
	}
	if got := byID["fallback"]["context_length"]; got != 1048576 {
		t.Fatalf("fallback context_length = %v, want 1048576", got)
	}
	if got := byID["fallback"]["max_completion_tokens"]; got != 65536 {
		t.Fatalf("fallback max_completion_tokens = %v, want 65536", got)
	}
	if _, exists := byID["without-limits"]["context_length"]; exists {
		t.Fatalf("model without limits exposed context_length: %#v", byID["without-limits"])
	}
}

func TestGetAvailableModelsOpenAIAggregatesConservativeMinimum(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("vertex-client", "vertex", []*ModelInfo{{
		ID: "shared-model", InputTokenLimit: 1048576, MaxContextLength: 1048576, OutputTokenLimit: 65536,
	}})
	r.RegisterClient("aistudio-client", "aistudio", []*ModelInfo{{
		ID: "shared-model", InputTokenLimit: 1048576, MaxContextLength: 131072, OutputTokenLimit: 8192,
	}})
	r.RegisterClient("standard-client", "openai", []*ModelInfo{{
		ID: "shared-model",
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("models = %#v, want one model", models)
	}
	if got := models[0]["context_length"]; got != 1048576 {
		t.Fatalf("context_length = %v, want 1048576", got)
	}
	if got := models[0]["max_context_length"]; got != 131072 {
		t.Fatalf("max_context_length = %v, want conservative 131072", got)
	}
	if got := models[0]["max_completion_tokens"]; got != 8192 {
		t.Fatalf("max_completion_tokens = %v, want conservative 8192", got)
	}
}

func TestGetAvailableModelsOpenAIDoesNotRestoreStaleRawLimitsAfterUnregister(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-with-limits", "OpenAI", []*ModelInfo{
		{ID: "transient-model", InputTokenLimit: 1048576, OutputTokenLimit: 65536},
	})
	r.RegisterClient("client-without-limits", "OpenAI", []*ModelInfo{
		{ID: "transient-model"},
	})

	r.UnregisterClient("client-with-limits")

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("models = %#v, want one model", models)
	}
	if _, exists := models[0]["context_length"]; exists {
		t.Fatalf("stale context_length restored after unregister: %#v", models[0])
	}
	if _, exists := models[0]["max_completion_tokens"]; exists {
		t.Fatalf("stale max_completion_tokens restored after unregister: %#v", models[0])
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
