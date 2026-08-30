package registry

import "testing"

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

func TestAntigravityWebSearchModelForRequiresAllEligibleClients(t *testing.T) {
	registryRef := GetGlobalRegistry()
	const (
		mixedModelID       = "gemini-mixed-web-search-safety-test"
		allCapableModelID  = "gemini-all-web-search-safety-test"
		mixedDisabledID    = "test-antigravity-mixed-web-search-disabled"
		mixedEnabledID     = "test-antigravity-mixed-web-search-enabled"
		allCapableFirstID  = "test-antigravity-all-web-search-first"
		allCapableSecondID = "test-antigravity-all-web-search-second"
	)
	registryRef.RegisterClient(mixedDisabledID, "antigravity", []*ModelInfo{{ID: mixedModelID}})
	registryRef.RegisterClient(mixedEnabledID, "antigravity", []*ModelInfo{{ID: mixedModelID, SupportsWebSearch: true}})
	registryRef.RegisterClient(allCapableFirstID, "antigravity", []*ModelInfo{{ID: allCapableModelID, SupportsWebSearch: true}})
	registryRef.RegisterClient(allCapableSecondID, "antigravity", []*ModelInfo{{ID: allCapableModelID, SupportsWebSearch: true}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(mixedDisabledID)
		registryRef.UnregisterClient(mixedEnabledID)
		registryRef.UnregisterClient(allCapableFirstID)
		registryRef.UnregisterClient(allCapableSecondID)
	})

	if got := registryRef.AllEligibleClientsSupportWebSearchModel("antigravity", mixedModelID); got {
		t.Fatal("mixed capability model should not be globally safe for native web search")
	}
	if got := AntigravityWebSearchModelFor(mixedModelID); got != "" {
		t.Fatalf("mixed capability model enabled native web search: %q", got)
	}
	if got := registryRef.AllEligibleClientsSupportWebSearchModel("antigravity", allCapableModelID); !got {
		t.Fatal("all-capable model should be globally safe for native web search")
	}
	if got := AntigravityWebSearchModelFor(allCapableModelID); got != allCapableModelID {
		t.Fatalf("all-capable model native web-search model = %q, want %q", got, allCapableModelID)
	}
}

func TestAntigravityWebSearchModelForIncludesHiddenCatalogModels(t *testing.T) {
	registryRef := GetGlobalRegistry()
	const (
		clientID = "test-antigravity-hidden-web-search"
		modelID  = "gemini-hidden-web-search-test"
	)
	registryRef.UnregisterClient(clientID)
	registryRef.RegisterClient(clientID, "antigravity", []*ModelInfo{{
		ID:                     modelID,
		SupportsWebSearch:      true,
		HiddenFromModelCatalog: true,
	}})
	t.Cleanup(func() { registryRef.UnregisterClient(clientID) })

	for _, model := range registryRef.GetAvailableModelsByProvider("antigravity") {
		if model != nil && model.ID == modelID {
			t.Fatalf("hidden model returned by public catalog: %#v", model)
		}
	}
	if got := AntigravityWebSearchModelFor(modelID); got != modelID {
		t.Fatalf("hidden capable model = %q, want %q", got, modelID)
	}
}
