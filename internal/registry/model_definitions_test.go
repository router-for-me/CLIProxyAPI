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

func TestGetStaticModelDefinitionsByChannelCopilotAliases(t *testing.T) {
	want := GetCopilotModels()
	if len(want) == 0 {
		t.Fatal("GetCopilotModels returned no models")
	}

	wantIDs := modelIDSet(want)
	required := []string{
		"claude-fable-5",
		"claude-opus-4-6",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"gemini-3.1-pro-preview",
		"gemini-3.5-flash",
		"mai-code-1-flash",
		"kimi-k2.7-code",
		"gpt-5.5",
		"gpt-5.6-luna",
		"gpt-5.6-sol",
	}
	for _, modelID := range required {
		if _, ok := wantIDs[modelID]; !ok {
			t.Fatalf("GetCopilotModels missing required model %q", modelID)
		}
	}

	for _, channel := range []string{"copilot", "github-copilot", "github_copilot"} {
		got := GetStaticModelDefinitionsByChannel(channel)
		if len(got) != len(want) {
			t.Fatalf("GetStaticModelDefinitionsByChannel(%q) length = %d, want %d", channel, len(got), len(want))
		}
		gotIDs := modelIDSet(got)
		for modelID := range wantIDs {
			if _, ok := gotIDs[modelID]; !ok {
				t.Fatalf("GetStaticModelDefinitionsByChannel(%q) missing model %q", channel, modelID)
			}
		}
	}
}

func modelIDSet(models []*ModelInfo) map[string]struct{} {
	ids := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil || model.ID == "" {
			continue
		}
		ids[model.ID] = struct{}{}
	}
	return ids
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
