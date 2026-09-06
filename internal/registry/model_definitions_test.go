package registry

import (
	"strings"
	"testing"
)

func TestGetStaticModelDefinitionsByChannelSupportsGeminiInteractions(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("gemini-interactions")
	if len(models) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(gemini-interactions) returned no models")
	}
}

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

func TestIsImageOnlyModel(t *testing.T) {
	modelRegistry := GetGlobalRegistry()
	const dynamicImageModelID = "registry-dynamic-image-model"
	modelRegistry.RegisterClient("registry-dynamic-image-client", "openai-compatibility", []*ModelInfo{{
		ID:   dynamicImageModelID,
		Type: OpenAIImageModelType,
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("registry-dynamic-image-client")
	})

	if !IsImageOnlyModel(dynamicImageModelID) {
		t.Fatalf("IsImageOnlyModel(%q) = false, want true", dynamicImageModelID)
	}

	for _, id := range []string{
		codexBuiltinImage15ModelID,
		codexBuiltinImageModelID,
		xaiBuiltinImageModelID,
		xaiBuiltinImageQualityModelID,
		xaiBuiltinImage20ModelID,
	} {
		if !IsImageOnlyModel(id) {
			t.Fatalf("IsImageOnlyModel(%q) = false, want true", id)
		}
		if !IsImageOnlyModel(" " + strings.ToUpper(id) + " ") {
			t.Fatalf("IsImageOnlyModel(%q) must ignore case and surrounding space", id)
		}
	}

	// Video built-ins and chat models keep their existing routing.
	for _, id := range []string{
		xaiBuiltinVideoModelID,
		xaiBuiltinVideo15ModelID,
		xaiBuiltinVideo15PreviewID,
		"gpt-5.6-luna",
		"grok-imagine",
		"gpt-image",
		"",
	} {
		if IsImageOnlyModel(id) {
			t.Fatalf("IsImageOnlyModel(%q) = true, want false", id)
		}
	}
}

// TestImageOnlyBuiltinsAreRegistered keeps the image-only set in sync with the
// built-in definitions: every entry must still be injected by WithCodexBuiltins
// or WithXAIBuiltins, so a renamed built-in cannot silently stop being rejected
// on chat-style endpoints.
func TestImageOnlyBuiltinsAreRegistered(t *testing.T) {
	registered := make(map[string]struct{})
	for _, model := range append(WithCodexBuiltins(nil), WithXAIBuiltins(nil)...) {
		if model == nil {
			continue
		}
		registered[strings.ToLower(strings.TrimSpace(model.ID))] = struct{}{}
	}
	for id := range imageOnlyBuiltinModelIDs {
		if _, ok := registered[id]; !ok {
			t.Fatalf("image-only model %q is not registered as a built-in", id)
		}
	}
}
