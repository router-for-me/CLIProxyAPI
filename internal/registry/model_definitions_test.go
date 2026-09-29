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

func TestGetNeuralwattModels(t *testing.T) {
	models := GetNeuralwattModels()
	if len(models) == 0 {
		t.Fatal("GetNeuralwattModels returned 0 models")
	}
	for i, model := range models {
		if model == nil {
			t.Fatalf("GetNeuralwattModels[%d] = nil", i)
		}
		if strings.TrimSpace(model.ID) == "" {
			t.Fatalf("GetNeuralwattModels[%d].ID is empty", i)
		}
		if strings.TrimSpace(model.Type) != "neuralwatt" {
			t.Fatalf("GetNeuralwattModels[%d].Type = %q, want %q", i, model.Type, "neuralwatt")
		}
		if strings.TrimSpace(model.OwnedBy) == "" {
			t.Fatalf("GetNeuralwattModels[%d].OwnedBy is empty", i)
		}
	}
}

func TestGetModelsByTypeNeuralwatt(t *testing.T) {
	direct := GetNeuralwattModels()
	byType := GetStaticModelDefinitionsByChannel("neuralwatt")
	if len(byType) != len(direct) {
		t.Fatalf("GetStaticModelDefinitionsByChannel(%q) = %d; GetNeuralwattModels = %d", "neuralwatt", len(byType), len(direct))
	}
	for i := range byType {
		if byType[i] == nil || direct[i] == nil {
			t.Fatalf("idx %d: nil model (byType=%v direct=%v)", i, byType[i], direct[i])
		}
		if byType[i].ID != direct[i].ID {
			t.Fatalf("idx %d: byType.ID = %q, want %q", i, byType[i].ID, direct[i].ID)
		}
	}
}

func TestLookupStaticModelInfoNeuralwatt(t *testing.T) {
	models := GetNeuralwattModels()
	if len(models) == 0 {
		t.Fatal("GetNeuralwattModels returned 0 models")
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		got := LookupStaticModelInfo(model.ID)
		if got == nil {
			t.Fatalf("LookupStaticModelInfo(%q) = nil", model.ID)
		}
		if got.ID != model.ID {
			t.Fatalf("LookupStaticModelInfo(%q).ID = %q, want %q", model.ID, got.ID, model.ID)
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
