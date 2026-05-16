package registry

import (
	"testing"
	"time"
)

func TestGetModelInfoReturnsClone(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Min: 1, Max: 2, Levels: []string{"low", "high"}},
	}})

	first := r.GetModelInfo("m1", "gemini")
	if first == nil {
		t.Fatal("expected model info")
	}
	first.DisplayName = "mutated"
	first.Thinking.Levels[0] = "mutated"

	second := r.GetModelInfo("m1", "gemini")
	if second.DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second.DisplayName)
	}
	if second.Thinking == nil || len(second.Thinking.Levels) == 0 || second.Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second.Thinking)
	}
}

func TestGetModelsForClientReturnsClones(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Levels: []string{"low", "high"}},
	}})

	first := r.GetModelsForClient("client-1")
	if len(first) != 1 || first[0] == nil {
		t.Fatalf("expected one model, got %+v", first)
	}
	first[0].DisplayName = "mutated"
	first[0].Thinking.Levels[0] = "mutated"

	second := r.GetModelsForClient("client-1")
	if len(second) != 1 || second[0] == nil {
		t.Fatalf("expected one model on second fetch, got %+v", second)
	}
	if second[0].DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second[0].DisplayName)
	}
	if second[0].Thinking == nil || len(second[0].Thinking.Levels) == 0 || second[0].Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second[0].Thinking)
	}
}

func TestGetAvailableModelsByProviderReturnsClones(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Levels: []string{"low", "high"}},
	}})

	first := r.GetAvailableModelsByProvider("gemini")
	if len(first) != 1 || first[0] == nil {
		t.Fatalf("expected one model, got %+v", first)
	}
	first[0].DisplayName = "mutated"
	first[0].Thinking.Levels[0] = "mutated"

	second := r.GetAvailableModelsByProvider("gemini")
	if len(second) != 1 || second[0] == nil {
		t.Fatalf("expected one model on second fetch, got %+v", second)
	}
	if second[0].DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second[0].DisplayName)
	}
	if second[0].Thinking == nil || len(second[0].Thinking.Levels) == 0 || second[0].Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second[0].Thinking)
	}
}

func TestCleanupExpiredQuotasInvalidatesAvailableModelsCache(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1", Created: 1}})
	r.SetModelQuotaExceeded("client-1", "m1")
	if models := r.GetAvailableModels("openai"); len(models) != 1 {
		t.Fatalf("expected cooldown model to remain listed before cleanup, got %d", len(models))
	}

	r.mutex.Lock()
	quotaTime := time.Now().Add(-6 * time.Minute)
	r.models["m1"].QuotaExceededClients["client-1"] = &quotaTime
	r.mutex.Unlock()

	r.CleanupExpiredQuotas()

	if count := r.GetModelCount("m1"); count != 1 {
		t.Fatalf("expected model count 1 after cleanup, got %d", count)
	}
	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected model to stay available after cleanup, got %d", len(models))
	}
	if got := models[0]["id"]; got != "m1" {
		t.Fatalf("expected model id m1, got %v", got)
	}
}

func TestGetAvailableModelsReturnsClonedSupportedParameters(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:                  "m1",
		DisplayName:         "Model One",
		SupportedParameters: []string{"temperature", "top_p"},
	}})

	first := r.GetAvailableModels("openai")
	if len(first) != 1 {
		t.Fatalf("expected one model, got %d", len(first))
	}
	params, ok := first[0]["supported_parameters"].([]string)
	if !ok || len(params) != 2 {
		t.Fatalf("expected supported_parameters slice, got %#v", first[0]["supported_parameters"])
	}
	params[0] = "mutated"

	second := r.GetAvailableModels("openai")
	params, ok = second[0]["supported_parameters"].([]string)
	if !ok || len(params) != 2 || params[0] != "temperature" {
		t.Fatalf("expected cloned supported_parameters, got %#v", second[0]["supported_parameters"])
	}
}

func TestGetAvailableModelsOpenAIIncludesModelDetailsAndThinking(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:                         "m1",
		OwnedBy:                    "team-a",
		Type:                       "openai",
		DisplayName:                "Model One",
		Version:                    "v1",
		Description:                "Detailed model",
		ContextLength:              128000,
		MaxCompletionTokens:        32768,
		InputTokenLimit:            128000,
		OutputTokenLimit:           32768,
		SupportedParameters:        []string{"tools"},
		SupportedGenerationMethods: []string{"generateContent"},
		SupportedInputModalities:   []string{"TEXT", "IMAGE"},
		SupportedOutputModalities:  []string{"TEXT"},
		Thinking: &ThinkingSupport{
			Min:            128,
			Max:            32768,
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"low", "medium", "high"},
		},
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected one model, got %d", len(models))
	}
	model := models[0]

	checks := map[string]any{
		"context_length":        128000,
		"max_completion_tokens": 32768,
		"input_token_limit":     128000,
		"output_token_limit":    32768,
	}
	for key, want := range checks {
		if got := model[key]; got != want {
			t.Fatalf("expected %s=%v, got %#v", key, want, got)
		}
	}

	thinking, ok := model["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking map, got %#v", model["thinking"])
	}
	if thinking["min"] != 128 || thinking["max"] != 32768 {
		t.Fatalf("unexpected thinking range: %#v", thinking)
	}
	if thinking["zero_allowed"] != true || thinking["dynamic_allowed"] != true {
		t.Fatalf("unexpected thinking flags: %#v", thinking)
	}
	levels, ok := thinking["levels"].([]string)
	if !ok || len(levels) != 3 || levels[0] != "low" {
		t.Fatalf("unexpected thinking levels: %#v", thinking["levels"])
	}
	supportedLevels, ok := thinking["supported_levels"].([]string)
	if !ok || len(supportedLevels) != 5 || supportedLevels[0] != "none" || supportedLevels[1] != "auto" || supportedLevels[2] != "low" {
		t.Fatalf("unexpected supported thinking levels: %#v", thinking["supported_levels"])
	}
	levelBudgets, ok := thinking["level_budgets"].(map[string]any)
	if !ok || levelBudgets["low"] != 1024 || levelBudgets["high"] != 24576 {
		t.Fatalf("unexpected thinking level budgets: %#v", thinking["level_budgets"])
	}

	levels[0] = "mutated"
	supportedLevels[0] = "mutated"
	second := r.GetAvailableModels("openai")
	secondThinking := second[0]["thinking"].(map[string]any)
	secondLevels := secondThinking["levels"].([]string)
	if secondLevels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %#v", secondLevels)
	}
	secondSupportedLevels := secondThinking["supported_levels"].([]string)
	if secondSupportedLevels[0] != "none" {
		t.Fatalf("expected cloned supported thinking levels, got %#v", secondSupportedLevels)
	}
}

func TestLookupModelInfoReturnsCloneForStaticDefinitions(t *testing.T) {
	first := LookupModelInfo("claude-sonnet-4-6")
	if first == nil || first.Thinking == nil || len(first.Thinking.Levels) == 0 {
		t.Fatalf("expected static model with thinking levels, got %+v", first)
	}
	first.Thinking.Levels[0] = "mutated"

	second := LookupModelInfo("claude-sonnet-4-6")
	if second == nil || second.Thinking == nil || len(second.Thinking.Levels) == 0 || second.Thinking.Levels[0] == "mutated" {
		t.Fatalf("expected static lookup clone, got %+v", second)
	}
}
