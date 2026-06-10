package registry

import "testing"

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

func TestIsAntigravityWebSearchModelUsesRuntimeCapability(t *testing.T) {
	registryRef := GetGlobalRegistry()
	registryRef.RegisterClient("test-antigravity-websearch", "antigravity", []*ModelInfo{
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
		{ID: "gemini-web-search-disabled", SupportsWebSearch: false},
	})
	registryRef.RegisterClient("test-gemini-websearch", "gemini", []*ModelInfo{
		{ID: "gemini-web-search-cross-provider", SupportsWebSearch: true},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient("test-antigravity-websearch")
		registryRef.UnregisterClient("test-gemini-websearch")
	})

	if !IsAntigravityWebSearchModel("gemini-web-search-test") {
		t.Fatal("runtime Antigravity web search model should be marked capable")
	}
	if !IsAntigravityWebSearchModel("gemini-web-search-test(high)") {
		t.Fatal("thinking suffix should not hide Antigravity web search capability")
	}
	if IsAntigravityWebSearchModel("gemini-web-search-disabled") {
		t.Fatal("Antigravity model without web search support should not be marked capable")
	}
	if IsAntigravityWebSearchModel("gemini-web-search-cross-provider") {
		t.Fatal("same capability on another provider should not mark Antigravity capable")
	}
}

func TestAntigravityWebSearchModelsUsesRuntimeCapability(t *testing.T) {
	registryRef := GetGlobalRegistry()
	registryRef.RegisterClient("test-antigravity-websearch-list", "antigravity", []*ModelInfo{
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
		{ID: "gemini-web-search-disabled", SupportsWebSearch: false},
	})
	t.Cleanup(func() {
		registryRef.UnregisterClient("test-antigravity-websearch-list")
	})

	models := AntigravityWebSearchModels()
	if len(models) == 0 {
		t.Fatal("expected at least one Antigravity web search model")
	}
	for _, model := range models {
		if model == "gemini-web-search-test" {
			return
		}
	}
	t.Fatalf("AntigravityWebSearchModels() = %#v, want gemini-web-search-test", models)
}

func TestAntigravityWebSearchModelForUsesRuntimeWebSearchModel(t *testing.T) {
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

	if got := AntigravityWebSearchModelFor("gemini-route-test"); got != "gemini-web-search-test" {
		t.Fatalf("AntigravityWebSearchModelFor route = %q, want gemini-web-search-test", got)
	}
	if got := AntigravityWebSearchModelFor("gemini-route-test(high)"); got != "gemini-web-search-test" {
		t.Fatalf("AntigravityWebSearchModelFor suffix route = %q, want gemini-web-search-test", got)
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
