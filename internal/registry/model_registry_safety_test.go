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

func TestLookupModelInfoReturnsCloneForStaticDefinitions(t *testing.T) {
	first := LookupModelInfo("glm-4.6")
	if first == nil || first.Thinking == nil || len(first.Thinking.Levels) == 0 {
		t.Fatalf("expected static model with thinking levels, got %+v", first)
	}
	first.Thinking.Levels[0] = "mutated"

	second := LookupModelInfo("glm-4.6")
	if second == nil || second.Thinking == nil || len(second.Thinking.Levels) == 0 || second.Thinking.Levels[0] == "mutated" {
		t.Fatalf("expected static lookup clone, got %+v", second)
	}
}

func TestLookupModelInfoUsesProviderScopedStaticCatalogForDuplicateIDs(t *testing.T) {
	t.Run("claude versus antigravity duplicate", func(t *testing.T) {
		claude := LookupModelInfo("claude-sonnet-4-6", "claude")
		if claude == nil || claude.Type != "claude" || claude.Thinking == nil {
			t.Fatalf("expected claude model info, got %+v", claude)
		}
		if claude.Thinking.Max != 128000 {
			t.Fatalf("expected claude max thinking 128000, got %d", claude.Thinking.Max)
		}
		if len(claude.Thinking.Levels) != 3 {
			t.Fatalf("expected claude levels, got %+v", claude.Thinking)
		}

		antigravity := LookupModelInfo("claude-sonnet-4-6", "antigravity")
		if antigravity == nil || antigravity.Type != "antigravity" || antigravity.Thinking == nil {
			t.Fatalf("expected antigravity model info, got %+v", antigravity)
		}
		if antigravity.Thinking.Max != 64000 {
			t.Fatalf("expected antigravity max thinking 64000, got %d", antigravity.Thinking.Max)
		}
		if len(antigravity.Thinking.Levels) != 0 {
			t.Fatalf("expected antigravity to use dynamic thinking without levels, got %+v", antigravity.Thinking)
		}
	})

	t.Run("gemini versus aistudio duplicate", func(t *testing.T) {
		gemini := LookupModelInfo("gemini-3-pro-preview", "gemini")
		if gemini == nil || gemini.Type != "gemini" || gemini.Thinking == nil {
			t.Fatalf("expected gemini model info, got %+v", gemini)
		}
		if len(gemini.Thinking.Levels) != 2 || gemini.Thinking.Levels[0] != "low" || gemini.Thinking.Levels[1] != "high" {
			t.Fatalf("expected gemini low/high levels, got %+v", gemini.Thinking)
		}

		aistudio := LookupModelInfo("gemini-3-pro-preview", "aistudio")
		if aistudio == nil || aistudio.Type != "gemini" || aistudio.Thinking == nil {
			t.Fatalf("expected aistudio-backed model info, got %+v", aistudio)
		}
		if len(aistudio.Thinking.Levels) != 0 {
			t.Fatalf("expected aistudio variant without discrete levels, got %+v", aistudio.Thinking)
		}
		if !aistudio.Thinking.DynamicAllowed {
			t.Fatalf("expected aistudio dynamic thinking support, got %+v", aistudio.Thinking)
		}
	})
}

func TestLookupModelInfoKnownProviderMissReturnsNil(t *testing.T) {
	if got := LookupModelInfo("gemini-3-pro-preview", "claude"); got != nil {
		t.Fatalf("expected nil for known-provider miss on cross-catalog ID, got %+v", got)
	}
}
