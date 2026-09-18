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

func TestGetZCodeModels(t *testing.T) {
	models := GetZCodeModels()
	if len(models) == 0 {
		t.Fatal("no zcode models")
	}
	if models[0].ID == "" {
		t.Fatal("model missing id")
	}
}

// Every zcode model must declare a thinking capability: without it the thinking
// pipeline resolves the model and then strips all thinking config (including the
// model[level] suffix), silently disabling reasoning on an endpoint that supports
// it (verified against the BigModel Anthropic endpoint, which returns thinking
// blocks).
func TestGetZCodeModels_DeclareThinking(t *testing.T) {
	for _, m := range GetZCodeModels() {
		if m == nil {
			continue
		}
		if m.Thinking == nil {
			t.Errorf("zcode model %q declares no thinking capability; thinking config would be stripped", m.ID)
			continue
		}
		if m.Thinking.Min <= 0 || m.Thinking.Max < m.Thinking.Min {
			t.Errorf("zcode model %q thinking range invalid: min=%d max=%d", m.ID, m.Thinking.Min, m.Thinking.Max)
		}
	}
}
