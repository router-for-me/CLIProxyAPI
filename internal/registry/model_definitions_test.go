package registry

import "testing"

func TestModelOverrideHeadersFromEmbeddedModels(t *testing.T) {
	const wantUA = "codex-tui/0.144.0 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.144.0)"
	got := ModelOverrideHeaders("gpt-5.6-luna")
	if got == nil {
		t.Fatal("ModelOverrideHeaders(gpt-5.6-luna) = nil, want headers")
	}
	if got["user-agent"] != wantUA {
		t.Fatalf("user-agent = %q, want %q", got["user-agent"], wantUA)
	}
	if got := ModelOverrideHeaders("gpt-5.4"); got != nil {
		t.Fatalf("ModelOverrideHeaders(gpt-5.4) = %#v, want nil", got)
	}
}

func TestGeminiVertexModelsUseFlashLiteReleaseID(t *testing.T) {
	const releaseID = "gemini-3.1-flash-lite"
	const previewID = releaseID + "-preview"

	for _, model := range GetGeminiVertexModels() {
		if model == nil {
			continue
		}
		if model.ID == previewID {
			t.Fatalf("Vertex model ID = %q, want release ID %q", model.ID, releaseID)
		}
		if model.ID == releaseID {
			return
		}
	}

	t.Fatalf("Vertex models do not contain %q", releaseID)
}

func TestWithKimiBuiltinsIncludesK31MModel(t *testing.T) {
	models := WithKimiBuiltins(nil)

	var found *ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == kimiBuiltinK31MModelID {
			found = model
			break
		}
	}
	if found == nil {
		t.Fatalf("expected Kimi builtin model %s", kimiBuiltinK31MModelID)
	}
	if found.ContextLength != 1048576 {
		t.Fatalf("context_length = %d, want 1048576", found.ContextLength)
	}
	if found.Thinking == nil {
		t.Fatal("thinking = nil, want level-based thinking support")
	}
	wantLevels := []string{"low", "high", "max"}
	if len(found.Thinking.Levels) != len(wantLevels) {
		t.Fatalf("thinking.levels = %v, want %v", found.Thinking.Levels, wantLevels)
	}
	for i, level := range wantLevels {
		if found.Thinking.Levels[i] != level {
			t.Fatalf("thinking.levels = %v, want %v", found.Thinking.Levels, wantLevels)
		}
	}
	wantMapping := map[string]string{"medium": "high", "xhigh": "max"}
	if len(found.Thinking.LevelMapping) != len(wantMapping) {
		t.Fatalf("thinking.level_mapping = %v, want %v", found.Thinking.LevelMapping, wantMapping)
	}
	for from, to := range wantMapping {
		if found.Thinking.LevelMapping[from] != to {
			t.Fatalf("thinking.level_mapping = %v, want %v", found.Thinking.LevelMapping, wantMapping)
		}
	}
}

func TestGetKimiModelsIncludesK31MBuiltin(t *testing.T) {
	for _, model := range GetKimiModels() {
		if model != nil && model.ID == kimiBuiltinK31MModelID {
			return
		}
	}

	t.Fatalf("expected GetKimiModels to include builtin model %s", kimiBuiltinK31MModelID)
}

func TestWithKimiBuiltinsIncludesK3Model(t *testing.T) {
	models := WithKimiBuiltins(nil)

	var found *ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == kimiBuiltinK3ModelID {
			found = model
			break
		}
	}
	if found == nil {
		t.Fatalf("expected Kimi builtin model %s", kimiBuiltinK3ModelID)
	}
	if found.ContextLength != 1048576 {
		t.Fatalf("context_length = %d, want 1048576", found.ContextLength)
	}
	if found.Thinking == nil {
		t.Fatal("thinking = nil, want level-based thinking support")
	}
	wantLevels := []string{"low", "high", "max"}
	if len(found.Thinking.Levels) != len(wantLevels) {
		t.Fatalf("thinking.levels = %v, want %v", found.Thinking.Levels, wantLevels)
	}
	for i, level := range wantLevels {
		if found.Thinking.Levels[i] != level {
			t.Fatalf("thinking.levels = %v, want %v", found.Thinking.Levels, wantLevels)
		}
	}
	wantMapping := map[string]string{"medium": "high", "xhigh": "max"}
	if len(found.Thinking.LevelMapping) != len(wantMapping) {
		t.Fatalf("thinking.level_mapping = %v, want %v", found.Thinking.LevelMapping, wantMapping)
	}
	for from, to := range wantMapping {
		if found.Thinking.LevelMapping[from] != to {
			t.Fatalf("thinking.level_mapping = %v, want %v", found.Thinking.LevelMapping, wantMapping)
		}
	}
}

func TestGetKimiModelsIncludesK3Builtin(t *testing.T) {
	for _, model := range GetKimiModels() {
		if model != nil && model.ID == kimiBuiltinK3ModelID {
			return
		}
	}

	t.Fatalf("expected GetKimiModels to include builtin model %s", kimiBuiltinK3ModelID)
}

func TestWithXAIBuiltinsIncludesVideoPreviewModel(t *testing.T) {
	models := WithXAIBuiltins(nil)

	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == xaiBuiltinVideo15PreviewModelID {
			return
		}
	}

	t.Fatalf("expected xAI builtin model %s", xaiBuiltinVideo15PreviewModelID)
}

func TestAntigravityWebSearchModelForRequiresRequestedModelCapability(t *testing.T) {
	registryRef := GetGlobalRegistry()
	registryRef.RegisterClient("test-antigravity-websearch-route", "antigravity", []*ModelInfo{
		{ID: "gemini-route-test"},
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
	})
	registryRef.RegisterClient("test-gemini-websearch-route", "gemini", []*ModelInfo{
		{ID: "gemini-cross-provider-route"},
		{ID: "gemini-cross-provider-search", SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient("test-antigravity-websearch-route")
		registryRef.UnregisterClient("test-gemini-websearch-route")
	})

	if got := AntigravityWebSearchModelFor("gemini-route-test"); got != "" {
		t.Fatalf("route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-route-test(high)"); got != "" {
		t.Fatalf("suffix route model without web search support should not get fallback model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-web-search-test"); got != "gemini-web-search-test" {
		t.Fatalf("AntigravityWebSearchModelFor capable model = %q, want itself", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-cross-provider-route"); got != "" {
		t.Fatalf("cross-provider model should not get Antigravity web search model, got %q", got)
	}
	if got := AntigravityWebSearchModelFor("unknown-model"); got != "" {
		t.Fatalf("unknown model should not get Antigravity web search model, got %q", got)
	}
}
