package registry

import (
	"strings"
	"testing"
)

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

func TestGetKiroModelsReturnsStaticFallbackSet(t *testing.T) {
	models := GetKiroModels()
	if len(models) == 0 {
		t.Fatal("expected Kiro static fallback models")
	}

	seen := make(map[string]struct{}, len(models))
	required := map[string]bool{
		"kiro-claude-sonnet-4-6": false,
		"kiro-claude-sonnet-4-5": false,
		"kiro-claude-sonnet-4":   false,
		"kiro-claude-opus-4-7":   false,
		"kiro-claude-opus-4-6":   false,
		"kiro-claude-opus-4-5":   false,
		"kiro-claude-haiku-4-5":  false,
	}

	for _, model := range models {
		if model == nil {
			t.Fatal("expected no nil Kiro models")
		}
		if !strings.HasPrefix(model.ID, "kiro-") {
			t.Fatalf("expected Kiro model ID to use kiro- prefix, got %q", model.ID)
		}
		if model.Type != "kiro" {
			t.Fatalf("model %q type = %q, want kiro", model.ID, model.Type)
		}
		if model.Thinking == nil {
			t.Fatalf("model %q missing Kiro thinking metadata", model.ID)
		}
		if model.Thinking.Min != DefaultKiroThinkingSupport.Min ||
			model.Thinking.Max != DefaultKiroThinkingSupport.Max ||
			model.Thinking.ZeroAllowed != DefaultKiroThinkingSupport.ZeroAllowed ||
			model.Thinking.DynamicAllowed != DefaultKiroThinkingSupport.DynamicAllowed {
			t.Fatalf("model %q thinking = %+v, want %+v", model.ID, model.Thinking, DefaultKiroThinkingSupport)
		}
		if _, exists := seen[model.ID]; exists {
			t.Fatalf("duplicate Kiro model ID %q", model.ID)
		}
		seen[model.ID] = struct{}{}
		if _, ok := required[model.ID]; ok {
			required[model.ID] = true
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected fallback model %q", modelID)
		}
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
