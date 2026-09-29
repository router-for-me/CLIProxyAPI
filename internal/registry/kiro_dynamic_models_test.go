package registry

import "testing"

// resetKiroDynamicModels clears the live catalog so tests do not leak state.
func resetKiroDynamicModels(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		kiroDynamicModels.mu.Lock()
		kiroDynamicModels.models = nil
		kiroDynamicModels.mu.Unlock()
	})
}

func TestBuildKiroModelInfosKeepsAutoAliases(t *testing.T) {
	models := BuildKiroModelInfos([]KiroModelCatalogEntry{
		{ModelID: "auto", ModelName: "Auto", MaxInputTokens: 1000000, MaxOutputTokens: 64000},
		{ModelID: "claude-opus-5.5", ModelName: "Claude Opus 5.5", MaxInputTokens: 1000000, MaxOutputTokens: 128000},
	})

	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}

	for _, id := range []string{"auto", "kiro", "kiro-auto", "claude-opus-5.5"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("BuildKiroModelInfos() missing model %q", id)
		}
	}

	opus := byID["claude-opus-5.5"]
	if opus.ContextLength != 1000000 || opus.MaxCompletionTokens != 128000 {
		t.Fatalf("claude-opus-5.5 limits = %d/%d, want 1000000/128000", opus.ContextLength, opus.MaxCompletionTokens)
	}
	if opus.DisplayName != "Claude Opus 5.5 (Kiro)" {
		t.Fatalf("claude-opus-5.5 display name = %q", opus.DisplayName)
	}

	// The generic aliases must inherit the auto entry's limits so clients
	// targeting them are not capped by stale built-in values.
	for _, id := range []string{"kiro", "kiro-auto"} {
		alias := byID[id]
		if alias.ContextLength != 1000000 || alias.MaxCompletionTokens != 64000 {
			t.Fatalf("%s limits = %d/%d, want 1000000/64000", id, alias.ContextLength, alias.MaxCompletionTokens)
		}
	}
}

func TestBuildKiroModelInfosEmptyCatalogReturnsNil(t *testing.T) {
	if models := BuildKiroModelInfos(nil); models != nil {
		t.Fatalf("BuildKiroModelInfos(nil) = %#v, want nil", models)
	}
	if models := BuildKiroModelInfos([]KiroModelCatalogEntry{{ModelID: "   "}}); models != nil {
		t.Fatalf("BuildKiroModelInfos(blank id) = %#v, want nil", models)
	}
}

func TestGetKiroModelsPrefersLiveCatalog(t *testing.T) {
	resetKiroDynamicModels(t)

	if !SetKiroDynamicModels(BuildKiroModelInfos([]KiroModelCatalogEntry{
		{ModelID: "auto", ModelName: "Auto", MaxInputTokens: 1000000, MaxOutputTokens: 64000},
		{ModelID: "claude-opus-5.5", ModelName: "Claude Opus 5.5", MaxInputTokens: 1000000, MaxOutputTokens: 128000},
	})) {
		t.Fatal("SetKiroDynamicModels() = false, want true on first store")
	}

	models := GetKiroModels()
	found := false
	for _, model := range models {
		if model.ID == "claude-opus-5.5" {
			found = true
		}
		// Models retired upstream must disappear once the live catalog wins.
		if model.ID == "claude-3-5-sonnet" {
			t.Fatalf("GetKiroModels() still lists retired model %q", model.ID)
		}
	}
	if !found {
		t.Fatal("GetKiroModels() missing claude-opus-5.5 from live catalog")
	}
}

func TestSetKiroDynamicModelsDetectsNoChange(t *testing.T) {
	resetKiroDynamicModels(t)

	entries := []KiroModelCatalogEntry{
		{ModelID: "auto", ModelName: "Auto", MaxInputTokens: 1000000, MaxOutputTokens: 64000},
	}
	if !SetKiroDynamicModels(BuildKiroModelInfos(entries)) {
		t.Fatal("first SetKiroDynamicModels() = false, want true")
	}
	if SetKiroDynamicModels(BuildKiroModelInfos(entries)) {
		t.Fatal("repeat SetKiroDynamicModels() = true, want false for identical catalog")
	}
}

func TestSetKiroDynamicModelsEmptyClearsOverride(t *testing.T) {
	resetKiroDynamicModels(t)

	SetKiroDynamicModels(BuildKiroModelInfos([]KiroModelCatalogEntry{
		{ModelID: "claude-opus-5.5", ModelName: "Claude Opus 5.5", MaxInputTokens: 1000000, MaxOutputTokens: 128000},
	}))
	if !SetKiroDynamicModels(nil) {
		t.Fatal("SetKiroDynamicModels(nil) = false, want true when clearing a stored catalog")
	}
	if models := KiroDynamicModels(); models != nil {
		t.Fatalf("KiroDynamicModels() = %#v, want nil after clear", models)
	}
	// Falling back must still yield a usable catalog.
	if len(GetKiroModels()) == 0 {
		t.Fatal("GetKiroModels() = empty after clearing live catalog")
	}
}
