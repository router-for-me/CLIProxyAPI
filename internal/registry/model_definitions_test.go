package registry

import "testing"

func TestCodexFreeModelsExcludeGPT55(t *testing.T) {
	model := findModelInfo(GetCodexFreeModels(), "gpt-5.5")
	if model != nil {
		t.Fatal("expected codex free tier to NOT include gpt-5.5")
	}
}

func TestCodexStaticModelsIncludeGPT55(t *testing.T) {
	tierModels := map[string][]*ModelInfo{
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}

	for tier, models := range tierModels {
		t.Run(tier, func(t *testing.T) {
			model := findModelInfo(models, "gpt-5.5")
			if model == nil {
				t.Fatalf("expected codex %s tier to include gpt-5.5", tier)
			}
			assertGPT55ModelInfo(t, tier, model)
		})
	}

	model := LookupStaticModelInfo("gpt-5.5")
	if model == nil {
		t.Fatal("expected LookupStaticModelInfo to find gpt-5.5")
	}
	assertGPT55ModelInfo(t, "lookup", model)
}

func TestWithXAIBuiltinsAddsVideoModel(t *testing.T) {
	models := WithXAIBuiltins(nil)
	found := false
	for _, model := range models {
		if model != nil && model.ID == xaiBuiltinVideoModelID {
			found = true
			if model.OwnedBy != "xai" {
				t.Fatalf("OwnedBy = %q, want xai", model.OwnedBy)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s builtin model", xaiBuiltinVideoModelID)
	}
}

func TestAntigravityModelsIncludeGemini35FlashVariants(t *testing.T) {
	models := GetAntigravityModels()
	tests := map[string]string{
		"gemini-3.5-flash":        "Gemini 3.5 Flash",
		"gemini-3.5-flash-high":   "Gemini 3.5 Flash (High)",
		"gemini-3.5-flash-medium": "Gemini 3.5 Flash (Medium)",
	}

	for id, displayName := range tests {
		model := findModelInfo(models, id)
		if model == nil {
			t.Fatalf("expected Antigravity model %s", id)
		}
		if model.DisplayName != displayName {
			t.Fatalf("%s display name = %q, want %q", id, model.DisplayName, displayName)
		}
		if model.Thinking == nil {
			t.Fatalf("%s should keep Antigravity thinking support", id)
		}
		if !hasLevel(model.Thinking.Levels, "medium") || !hasLevel(model.Thinking.Levels, "high") {
			t.Fatalf("%s thinking levels = %v, want medium and high", id, model.Thinking.Levels)
		}
	}
}

func TestAntigravityModelsNormalizeLegacyGemini35Aliases(t *testing.T) {
	modelsCatalogStore.mu.Lock()
	original := modelsCatalogStore.data
	modelsCatalogStore.data = &staticModelsJSON{
		Claude:    []*ModelInfo{{ID: "claude-test"}},
		Gemini:    []*ModelInfo{{ID: "gemini-test"}},
		Vertex:    []*ModelInfo{{ID: "vertex-test"}},
		GeminiCLI: []*ModelInfo{{ID: "gemini-cli-test"}},
		AIStudio:  []*ModelInfo{{ID: "aistudio-test"}},
		CodexFree: []*ModelInfo{{ID: "codex-free-test"}},
		CodexTeam: []*ModelInfo{{ID: "codex-team-test"}},
		CodexPlus: []*ModelInfo{{ID: "codex-plus-test"}},
		CodexPro:  []*ModelInfo{{ID: "codex-pro-test"}},
		Kimi:      []*ModelInfo{{ID: "kimi-test"}},
		XAI:       []*ModelInfo{{ID: "xai-test"}},
		Antigravity: []*ModelInfo{
			{ID: "gemini-3-flash", Object: "model", OwnedBy: "antigravity", Type: "antigravity", DisplayName: "Gemini 3 Flash"},
			{ID: "gemini-3-flash-agent", Object: "model", OwnedBy: "antigravity", Type: "antigravity", DisplayName: "Gemini 3.5 Flash (High)"},
			{ID: "gemini-3.5-flash-low", Object: "model", OwnedBy: "antigravity", Type: "antigravity", DisplayName: "Gemini 3.5 Flash (Medium)"},
		},
	}
	modelsCatalogStore.mu.Unlock()

	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})

	models := GetAntigravityModels()
	for _, id := range []string{"gemini-3.5-flash", "gemini-3.5-flash-high", "gemini-3.5-flash-medium"} {
		if findModelInfo(models, id) == nil {
			t.Fatalf("expected canonical Antigravity model %s", id)
		}
		if LookupStaticModelInfo(id) == nil {
			t.Fatalf("expected static lookup for %s", id)
		}
	}

	for _, legacyID := range []string{"gemini-3-flash-agent", "gemini-3.5-flash-low"} {
		if findModelInfo(models, legacyID) != nil {
			t.Fatalf("did not expect legacy alias model %s in Antigravity models", legacyID)
		}
		if LookupStaticModelInfo(legacyID) != nil {
			t.Fatalf("did not expect static lookup to expose legacy alias %s", legacyID)
		}
	}
}

func TestValidateModelsCatalogAllowsMissingSections(t *testing.T) {
	data := validTestModelsCatalog()
	data.XAI = nil

	if err := validateModelsCatalog(data); err != nil {
		t.Fatalf("validateModelsCatalog() error = %v", err)
	}
}

func TestValidateModelsCatalogRejectsInvalidDefinitions(t *testing.T) {
	data := validTestModelsCatalog()
	data.Claude = []*ModelInfo{{ID: ""}}

	if err := validateModelsCatalog(data); err == nil {
		t.Fatal("expected invalid model definition error")
	}
}

func validTestModelsCatalog() *staticModelsJSON {
	models := []*ModelInfo{{ID: "test-model"}}
	return &staticModelsJSON{
		Claude:      models,
		Gemini:      models,
		Vertex:      models,
		GeminiCLI:   models,
		AIStudio:    models,
		CodexFree:   models,
		CodexTeam:   models,
		CodexPlus:   models,
		CodexPro:    models,
		Kimi:        models,
		Antigravity: models,
		XAI:         models,
	}
}

func hasLevel(levels []string, want string) bool {
	for _, level := range levels {
		if level == want {
			return true
		}
	}
	return false
}

func findModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func assertGPT55ModelInfo(t *testing.T, source string, model *ModelInfo) {
	t.Helper()

	if model.ID != "gpt-5.5" {
		t.Fatalf("%s id mismatch: got %q", source, model.ID)
	}
	if model.Object != "model" {
		t.Fatalf("%s object mismatch: got %q", source, model.Object)
	}
	if model.Created != 1776902400 {
		t.Fatalf("%s created timestamp mismatch: got %d", source, model.Created)
	}
	if model.OwnedBy != "openai" {
		t.Fatalf("%s owned_by mismatch: got %q", source, model.OwnedBy)
	}
	if model.Type != "openai" {
		t.Fatalf("%s type mismatch: got %q", source, model.Type)
	}
	if model.DisplayName != "GPT 5.5" {
		t.Fatalf("%s display name mismatch: got %q", source, model.DisplayName)
	}
	if model.Version != "gpt-5.5" {
		t.Fatalf("%s version mismatch: got %q", source, model.Version)
	}
	if model.Description != "Frontier model for complex coding, research, and real-world work." {
		t.Fatalf("%s description mismatch: got %q", source, model.Description)
	}
	if model.ContextLength != 272000 {
		t.Fatalf("%s context length mismatch: got %d", source, model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("%s max completion tokens mismatch: got %d", source, model.MaxCompletionTokens)
	}
	if len(model.SupportedParameters) != 1 || model.SupportedParameters[0] != "tools" {
		t.Fatalf("%s supported parameters mismatch: got %v", source, model.SupportedParameters)
	}
	if model.Thinking == nil {
		t.Fatalf("%s missing thinking support", source)
	}

	want := []string{"low", "medium", "high", "xhigh"}
	if len(model.Thinking.Levels) != len(want) {
		t.Fatalf("%s thinking level count mismatch: got %d, want %d", source, len(model.Thinking.Levels), len(want))
	}
	for i, level := range want {
		if model.Thinking.Levels[i] != level {
			t.Fatalf("%s thinking level %d mismatch: got %q, want %q", source, i, model.Thinking.Levels[i], level)
		}
	}
}
