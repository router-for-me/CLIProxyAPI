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

func TestGetAvailableModelsIncludesMaxContextLengthOverride(t *testing.T) {
	r := newTestModelRegistry()
	const want = 1048576
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:               "deepseek-v4-flash",
		ContextLength:    want,
		MaxContextLength: want,
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("models length = %d, want 1", len(models))
	}
	if got := models[0]["context_length"]; got != want {
		t.Fatalf("context_length = %#v, want %d", got, want)
	}
	if got := models[0]["max_context_length"]; got != want {
		t.Fatalf("max_context_length = %#v, want %d", got, want)
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

func TestLookupModelInfoIncludesClaudeSonnet5(t *testing.T) {
	model := LookupModelInfo("claude-sonnet-5")
	if model == nil {
		t.Fatal("expected Claude Sonnet 5 static model")
	}
	if model.Type != "claude" {
		t.Fatalf("Claude Sonnet 5 type = %q, want claude", model.Type)
	}
	if model.ContextLength != 1000000 {
		t.Fatalf("Claude Sonnet 5 context length = %d, want 1000000", model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("Claude Sonnet 5 max completion tokens = %d, want 128000", model.MaxCompletionTokens)
	}
	if model.Thinking == nil || !model.Thinking.ZeroAllowed || !model.Thinking.DynamicAllowed || model.Thinking.Min != 0 || model.Thinking.Max != 0 {
		t.Fatalf("expected Claude Sonnet 5 dynamic level-only thinking with zero allowed, got %+v", model.Thinking)
	}
	expectedLevels := []string{"low", "medium", "high", "xhigh", "max"}
	if len(model.Thinking.Levels) != len(expectedLevels) {
		t.Fatalf("Claude Sonnet 5 thinking levels = %+v, want %+v", model.Thinking.Levels, expectedLevels)
	}
	for i, level := range expectedLevels {
		if model.Thinking.Levels[i] != level {
			t.Fatalf("Claude Sonnet 5 thinking levels = %+v, want %+v", model.Thinking.Levels, expectedLevels)
		}
	}
}

func TestApplyClientModelCapabilities_UpdatesAllViews(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("ag-client-1", "antigravity", []*ModelInfo{{
		ID:                "gemini-3.1-flash-lite",
		SupportsWebSearch: false,
	}})

	epoch := r.ClientRegistrationEpoch("ag-client-1")
	applied := r.ApplyClientModelCapabilities("ag-client-1", epoch, func(modelID string, info *ModelInfo) {
		if modelID == "gemini-3.1-flash-lite" {
			info.SupportsWebSearch = true
		}
	})
	if !applied {
		t.Fatal("expected capabilities to be applied")
	}

	// 1. Check GetModelsForClient
	clientModels := r.GetModelsForClient("ag-client-1")
	if len(clientModels) != 1 || !clientModels[0].SupportsWebSearch {
		t.Fatalf("GetModelsForClient: expected SupportsWebSearch=true, got %+v", clientModels)
	}

	// 2. Check GetModelInfo (both with provider and global)
	infoByProv := r.GetModelInfo("gemini-3.1-flash-lite", "antigravity")
	if infoByProv == nil || !infoByProv.SupportsWebSearch {
		t.Fatalf("GetModelInfo with provider: expected SupportsWebSearch=true, got %+v", infoByProv)
	}
	infoGlobal := r.GetModelInfo("gemini-3.1-flash-lite", "")
	if infoGlobal == nil || !infoGlobal.SupportsWebSearch {
		t.Fatalf("GetModelInfo global: expected SupportsWebSearch=true, got %+v", infoGlobal)
	}

	// 3. Check GetAvailableModelInfos
	availInfos := r.GetAvailableModelInfos()
	foundAvail := false
	for _, m := range availInfos {
		if m.ID == "gemini-3.1-flash-lite" && m.SupportsWebSearch {
			foundAvail = true
			break
		}
	}
	if !foundAvail {
		t.Fatal("GetAvailableModelInfos: expected gemini-3.1-flash-lite with SupportsWebSearch=true")
	}
}

func TestMultiClientRegistration_PreservesProbedCapabilities(t *testing.T) {
	r := newTestModelRegistry()

	// 1. Client A registers model
	r.RegisterClient("client-A", "antigravity", []*ModelInfo{{
		ID:                "gemini-3.1-flash-lite",
		SupportsWebSearch: false,
	}})

	// A probes successfully -> SupportsWebSearch = true
	epochA := r.ClientRegistrationEpoch("client-A")
	r.ApplyClientModelCapabilities("client-A", epochA, func(modelID string, info *ModelInfo) {
		if modelID == "gemini-3.1-flash-lite" {
			info.SupportsWebSearch = true
		}
	})

	// 2. Client B registers the same model with static info (SupportsWebSearch = false)
	r.RegisterClient("client-B", "antigravity", []*ModelInfo{{
		ID:                "gemini-3.1-flash-lite",
		SupportsWebSearch: false,
	}})

	// Check that global views preserve SupportsWebSearch=true because Client A is still registered and has the capability
	info := r.GetModelInfo("gemini-3.1-flash-lite", "antigravity")
	if info == nil || !info.SupportsWebSearch {
		t.Fatalf("GetModelInfo: expected SupportsWebSearch=true to be preserved after client B registered, got %+v", info)
	}

	avail := r.GetAvailableModelInfos()
	foundSearch := false
	for _, m := range avail {
		if m.ID == "gemini-3.1-flash-lite" && m.SupportsWebSearch {
			foundSearch = true
			break
		}
	}
	if !foundSearch {
		t.Fatal("GetAvailableModelInfos: expected SupportsWebSearch=true to be preserved after client B registered")
	}

	// 3. Client A unregisters -> Now neither client supports web search, so global view should drop it
	r.UnregisterClient("client-A")
	infoAfterA := r.GetModelInfo("gemini-3.1-flash-lite", "antigravity")
	if infoAfterA == nil || infoAfterA.SupportsWebSearch {
		t.Fatalf("GetModelInfo: expected SupportsWebSearch=false after client A unregistered, got %+v", infoAfterA)
	}
}

func TestReRegisterClient_ClearsStaleProbedCapabilitiesWhenNoOtherClientSupports(t *testing.T) {
	r := newTestModelRegistry()

	// 1. Client A registers model and probes successfully (SupportsWebSearch=true)
	r.RegisterClient("client-A", "antigravity", []*ModelInfo{{
		ID:                "gemini-3.1-flash-lite",
		SupportsWebSearch: false,
	}})
	epochA := r.ClientRegistrationEpoch("client-A")
	r.ApplyClientModelCapabilities("client-A", epochA, func(modelID string, info *ModelInfo) {
		if modelID == "gemini-3.1-flash-lite" {
			info.SupportsWebSearch = true
		}
	})

	// 2. Client B registers the same model without web search support
	r.RegisterClient("client-B", "antigravity", []*ModelInfo{{
		ID:                "gemini-3.1-flash-lite",
		SupportsWebSearch: false,
	}})

	// 3. Client A now re-registers with new model definitions where SupportsWebSearch=false
	// (e.g. config reload or model catalog update)
	r.RegisterClient("client-A", "antigravity", []*ModelInfo{{
		ID:                "gemini-3.1-flash-lite",
		SupportsWebSearch: false,
	}})

	// Global view must NOT keep client A's stale pre-re-registration capability snapshot!
	// Since neither A nor B now has SupportsWebSearch=true, the global and provider view must be false.
	info := r.GetModelInfo("gemini-3.1-flash-lite", "antigravity")
	if info == nil || info.SupportsWebSearch {
		t.Fatalf("GetModelInfo: expected SupportsWebSearch=false after client A re-registered with false, got %+v", info)
	}

	avail := r.GetAvailableModelInfos()
	for _, m := range avail {
		if m.ID == "gemini-3.1-flash-lite" && m.SupportsWebSearch {
			t.Fatalf("GetAvailableModelInfos: expected SupportsWebSearch=false on gemini-3.1-flash-lite, got %+v", m)
		}
	}

	clientAModels := r.GetModelsForClient("client-A")
	if len(clientAModels) != 1 || clientAModels[0].SupportsWebSearch {
		t.Fatalf("GetModelsForClient(A): expected SupportsWebSearch=false, got %+v", clientAModels)
	}
}
