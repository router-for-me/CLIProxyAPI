package registry

import (
	"slices"
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

func TestCodexGPT56ThinkingLevelsMatchOfficialManifest(t *testing.T) {
	withUltra := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	withoutUltra := []string{"low", "medium", "high", "xhigh", "max"}
	tiers := []struct {
		name   string
		models []*ModelInfo
		want   map[string][]string
	}{
		{
			name:   "free",
			models: GetCodexFreeModels(),
			want: map[string][]string{
				"gpt-5.6-terra": withUltra,
				"gpt-5.6-luna":  withoutUltra,
			},
		},
		{
			name:   "team",
			models: GetCodexTeamModels(),
			want: map[string][]string{
				"gpt-5.6-sol":   withUltra,
				"gpt-5.6-terra": withUltra,
				"gpt-5.6-luna":  withoutUltra,
			},
		},
		{
			name:   "plus",
			models: GetCodexPlusModels(),
			want: map[string][]string{
				"gpt-5.6-sol":   withUltra,
				"gpt-5.6-terra": withUltra,
				"gpt-5.6-luna":  withoutUltra,
			},
		},
		{
			name:   "pro",
			models: GetCodexProModels(),
			want: map[string][]string{
				"gpt-5.6-sol":   withUltra,
				"gpt-5.6-terra": withUltra,
				"gpt-5.6-luna":  withoutUltra,
			},
		},
	}

	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			byID := make(map[string]*ModelInfo, len(tier.models))
			for _, model := range tier.models {
				if model != nil {
					byID[model.ID] = model
				}
			}
			for modelID, wantLevels := range tier.want {
				model := byID[modelID]
				if model == nil {
					t.Fatalf("model %q is missing", modelID)
				}
				if model.Thinking == nil {
					t.Fatalf("model %q thinking support = nil", modelID)
				}
				if !slices.Equal(model.Thinking.Levels, wantLevels) {
					t.Fatalf("model %q thinking levels = %v, want %v", modelID, model.Thinking.Levels, wantLevels)
				}
			}
		})
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
