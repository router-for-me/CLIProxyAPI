package registry

import "testing"

func TestGetStaticModelDefinitionsByChannelSupportsGeminiInteractions(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("gemini-interactions")
	if len(models) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(gemini-interactions) returned no models")
	}
}

func TestModelOverrideHeadersFromEmbeddedModels(t *testing.T) {
	const wantUA = "codex-tui/0.153.3 (Mac OS 26.5.1; arm64) iTerm.app/3.6.11 (codex-tui; 0.153.3)"
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

func TestWithXAIBuiltinsIncludesImage20(t *testing.T) {
	models := WithXAIBuiltins(nil)
	for _, model := range models {
		if model != nil && model.ID == xaiBuiltinImage20ModelID {
			if model.Created != 1786060800 {
				t.Fatalf("created = %d, want 1786060800 (2026-08-07)", model.Created)
			}
			return
		}
	}
	t.Fatalf("expected xAI builtin model %s", xaiBuiltinImage20ModelID)
}

func TestWithXAIBuiltinsIncludesVideo15GAAndPreviewAlias(t *testing.T) {
	models := WithXAIBuiltins(nil)
	foundGA := false
	foundPreviewAlias := false

	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == xaiBuiltinVideo15ModelID {
			foundGA = true
		}
		if model.ID == xaiBuiltinVideo15PreviewID {
			foundPreviewAlias = true
		}
	}

	if !foundGA {
		t.Fatalf("expected xAI builtin model %s", xaiBuiltinVideo15ModelID)
	}
	if !foundPreviewAlias {
		t.Fatalf("expected xAI builtin compatibility alias %s", xaiBuiltinVideo15PreviewID)
	}
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

func TestWithCodexBuiltinsIncludesImage25Models(t *testing.T) {
	models := WithCodexBuiltins(nil)
	expectedModels := map[string]string{
		"gpt-image-2.5-flare":    "GPT Image 2.5 Flare",
		"gpt-image-2.5-sunburst": "GPT Image 2.5 Sunburst",
		"gpt-image-2.5":          "GPT Image 2.5",
	}

	found := make(map[string]*ModelInfo)
	for _, model := range models {
		if model != nil {
			if _, ok := expectedModels[model.ID]; ok {
				found[model.ID] = model
			}
		}
	}

	for id, wantDisplayName := range expectedModels {
		model, ok := found[id]
		if !ok {
			t.Fatalf("expected builtin model %s in WithCodexBuiltins", id)
		}
		if model.DisplayName != wantDisplayName {
			t.Errorf("model %s DisplayName = %q, want %q", id, model.DisplayName, wantDisplayName)
		}
		if model.Object != "model" {
			t.Errorf("model %s Object = %q, want model", id, model.Object)
		}
		if model.OwnedBy != "openai" {
			t.Errorf("model %s OwnedBy = %q, want openai", id, model.OwnedBy)
		}
		if model.Type != "openai" {
			t.Errorf("model %s Type = %q, want openai", id, model.Type)
		}
		if model.Version != id {
			t.Errorf("model %s Version = %q, want %q", id, model.Version, id)
		}
		if model.Created != 1704067200 {
			t.Errorf("model %s Created = %d, want 1704067200", id, model.Created)
		}
	}
}

func TestGeneratedMediaBuiltinsDeclareOutputModalities(t *testing.T) {
	tests := []struct {
		name  string
		model *ModelInfo
		want  string
	}{
		{name: "codex image", model: codexBuiltinImageModelInfo(), want: "image"},
		{name: "codex image 2.5 flare", model: codexBuiltinImage25FlareModelInfo(), want: "image"},
		{name: "codex image 2.5 sunburst", model: codexBuiltinImage25SunburstModelInfo(), want: "image"},
		{name: "codex image 2.5", model: codexBuiltinImage25ModelInfo(), want: "image"},
		{name: "xai image", model: xaiBuiltinImageModelInfo(), want: "image"},
		{name: "xai video", model: xaiBuiltinVideoModelInfo(), want: "video"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.model.SupportedOutputModalities) != 1 || tt.model.SupportedOutputModalities[0] != tt.want {
				t.Fatalf("output modalities = %#v, want [%s]", tt.model.SupportedOutputModalities, tt.want)
			}
		})
	}
}

func TestStaticChatModelsDeclareTextModalities(t *testing.T) {
	for _, id := range []string{"claude-3-5-haiku-20241022", "grok-composer-2.5-fast"} {
		model := LookupStaticModelInfo(id)
		if model == nil {
			t.Fatalf("model %q missing", id)
		}
		if len(model.SupportedInputModalities) == 0 || len(model.SupportedOutputModalities) != 1 || model.SupportedOutputModalities[0] != "text" {
			t.Fatalf("model %q modalities = input %#v output %#v", id, model.SupportedInputModalities, model.SupportedOutputModalities)
		}
	}
}
