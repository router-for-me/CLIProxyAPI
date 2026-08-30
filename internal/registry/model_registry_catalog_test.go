package registry

import "testing"

func TestHiddenCatalogModelRemainsAvailableForRouting(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("prefixed-client", "openai-compatibility", []*ModelInfo{{
		ID:                     "minimaxai/minimax-m3",
		HiddenFromModelCatalog: true,
	}})

	if models := r.GetAvailableModels("openai"); len(models) != 0 {
		t.Fatalf("hidden model returned by catalog: %#v", models)
	}
	if infos := r.GetAvailableModelInfos(); len(infos) != 0 {
		t.Fatalf("hidden model returned by metadata catalog: %#v", infos)
	}
	if models := r.GetAvailableModelsByProvider("openai-compatibility"); len(models) != 0 {
		t.Fatalf("hidden provider model returned by catalog: %#v", models)
	}
	if !r.ClientSupportsModel("prefixed-client", "minimaxai/minimax-m3") {
		t.Fatal("hidden model is not available to its routing client")
	}
	if providers := r.GetModelProviders("minimaxai/minimax-m3"); len(providers) != 1 || providers[0] != "openai-compatibility" {
		t.Fatalf("providers = %#v, want the registered provider", providers)
	}
	if info := r.GetModelInfo("minimaxai/minimax-m3", "openai-compatibility"); info == nil {
		t.Fatal("hidden model metadata is not available to routing")
	}
}

func TestVisibleProviderKeepsSharedModelInCatalog(t *testing.T) {
	r := newTestModelRegistry()
	modelID := "shared-model"
	r.RegisterClient("prefixed-client", "openai-compatibility", []*ModelInfo{{
		ID:                     modelID,
		HiddenFromModelCatalog: true,
	}})
	r.RegisterClient("direct-client", "openai", []*ModelInfo{{ID: modelID}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 || models[0]["id"] != modelID {
		t.Fatalf("models = %#v, want one visible shared model", models)
	}

	infos := r.GetAvailableModelInfos()
	if len(infos) != 1 || infos[0].ID != modelID || infos[0].HiddenFromModelCatalog {
		t.Fatalf("infos = %#v, want one visible shared model", infos)
	}
}

func TestCatalogMetadataSelectionUsesStableClientIDPrecedence(t *testing.T) {
	const modelID = "shared-model"

	r := newTestModelRegistry()
	r.RegisterClient("z-client", "shared-provider", []*ModelInfo{{
		ID:               modelID,
		OwnedBy:          "z-owner",
		DisplayName:      "Z model",
		ContextLength:    128000,
		MaxContextLength: 64000,
	}})
	r.RegisterClient("a-client", "shared-provider", []*ModelInfo{{
		ID:               modelID,
		OwnedBy:          "a-owner",
		DisplayName:      "A model",
		ContextLength:    256000,
		MaxContextLength: 128000,
	}})

	for i := 0; i < 100; i++ {
		infos := r.GetAvailableModelInfos()
		if len(infos) != 1 {
			t.Fatalf("GetAvailableModelInfos() returned %d models, want 1: %#v", len(infos), infos)
		}
		assertSelectedCatalogMetadata(t, infos[0], "a-owner", "A model", 256000, 128000)
	}

	assertSelectedCatalogModelMap(t, r.GetAvailableModels("openai"), modelID, "a-owner", "A model", 256000, 128000)

	r.SuspendClientModel("a-client", modelID, "manual")
	assertSelectedCatalogModelMap(t, r.GetAvailableModels("openai"), modelID, "z-owner", "Z model", 128000, 64000)

	r.ResumeClientModel("a-client", modelID)
	assertSelectedCatalogModelMap(t, r.GetAvailableModels("openai"), modelID, "a-owner", "A model", 256000, 128000)
}

func TestCatalogVisibilityAggregatesActiveClientMetadata(t *testing.T) {
	const modelID = "shared-provider-model"

	orders := []struct {
		name        string
		firstHidden bool
	}{
		{name: "hidden then visible", firstHidden: true},
		{name: "visible then hidden", firstHidden: false},
	}
	for _, order := range orders {
		for _, removeVisibleFirst := range []bool{true, false} {
			t.Run(order.name+"/remove-visible-first="+boolString(removeVisibleFirst), func(t *testing.T) {
				r := newTestModelRegistry()
				registerCatalogVisibilityClients(r, modelID, order.firstHidden)
				assertCatalogModelVisibility(t, r, modelID, true)

				if removeVisibleFirst {
					r.UnregisterClient("visible-client")
					assertCatalogModelVisibility(t, r, modelID, false)
					r.UnregisterClient("hidden-client")
					assertCatalogModelVisibility(t, r, modelID, false)
					return
				}

				r.UnregisterClient("hidden-client")
				assertCatalogModelVisibility(t, r, modelID, true)
				r.UnregisterClient("visible-client")
				assertCatalogModelVisibility(t, r, modelID, false)
			})
		}
	}
}

func TestCatalogVisibilityIgnoresNonQuotaSuspendedClientMetadata(t *testing.T) {
	const modelID = "shared-provider-model"

	r := newTestModelRegistry()
	registerCatalogVisibilityClients(r, modelID, true)
	assertCatalogModelVisibility(t, r, modelID, true)

	r.SuspendClientModel("visible-client", modelID, "manual")
	assertCatalogModelVisibility(t, r, modelID, false)

	r.ResumeClientModel("visible-client", modelID)
	assertCatalogModelVisibility(t, r, modelID, true)
}

func TestCatalogCapabilityAggregationPreservesPerClientWebSearchMetadata(t *testing.T) {
	const modelID = "mixed-antigravity-web-search-model"

	r := newTestModelRegistry()
	r.RegisterClient("web-search-disabled-client", "antigravity", []*ModelInfo{{ID: modelID}})
	r.RegisterClient("web-search-enabled-client", "antigravity", []*ModelInfo{{ID: modelID, SupportsWebSearch: true}})

	if !r.ClientSupportsModel("web-search-disabled-client", modelID) || !r.ClientSupportsModel("web-search-enabled-client", modelID) {
		t.Fatal("both clients should support the shared model")
	}
	if r.ClientSupportsWebSearchModel("web-search-disabled-client", modelID) {
		t.Fatal("web-search-disabled-client should not advertise web search")
	}
	if !r.ClientSupportsWebSearchModel("web-search-enabled-client", modelID) {
		t.Fatal("web-search-enabled-client should advertise web search")
	}

	capabilities := r.GetAvailableModelCapabilitiesByProvider("antigravity")
	for _, model := range capabilities {
		if model != nil && model.ID == modelID {
			if !model.SupportsWebSearch {
				t.Fatal("aggregate capability catalog lost the advertised web-search capability")
			}
			return
		}
	}
	t.Fatalf("aggregate capability catalog does not contain %q: %#v", modelID, capabilities)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func registerCatalogVisibilityClients(r *ModelRegistry, modelID string, firstHidden bool) {
	hiddenModel := &ModelInfo{ID: modelID, DisplayName: "hidden", HiddenFromModelCatalog: true}
	visibleModel := &ModelInfo{ID: modelID, DisplayName: "visible"}
	if firstHidden {
		r.RegisterClient("hidden-client", "shared-provider", []*ModelInfo{hiddenModel})
		r.RegisterClient("visible-client", "shared-provider", []*ModelInfo{visibleModel})
		return
	}
	r.RegisterClient("visible-client", "shared-provider", []*ModelInfo{visibleModel})
	r.RegisterClient("hidden-client", "shared-provider", []*ModelInfo{hiddenModel})
}

func assertCatalogModelVisibility(t *testing.T, r *ModelRegistry, modelID string, wantVisible bool) {
	t.Helper()

	models := r.GetAvailableModels("openai")
	if got := catalogModelDisplayName(models, modelID); wantVisible && got != "visible" {
		t.Fatalf("GetAvailableModels() display name = %q, want visible; models = %#v", got, models)
	} else if !wantVisible && got != "" {
		t.Fatalf("GetAvailableModels() returned hidden model with display name %q: %#v", got, models)
	}

	infos := r.GetAvailableModelInfos()
	if got := modelInfoDisplayName(infos, modelID); wantVisible && got != "visible" {
		t.Fatalf("GetAvailableModelInfos() display name = %q, want visible; infos = %#v", got, infos)
	} else if !wantVisible && got != "" {
		t.Fatalf("GetAvailableModelInfos() returned hidden model with display name %q: %#v", got, infos)
	}

	providerModels := r.GetAvailableModelsByProvider("shared-provider")
	if got := modelInfoDisplayName(providerModels, modelID); wantVisible && got != "visible" {
		t.Fatalf("GetAvailableModelsByProvider() display name = %q, want visible; models = %#v", got, providerModels)
	} else if !wantVisible && got != "" {
		t.Fatalf("GetAvailableModelsByProvider() returned hidden model with display name %q: %#v", got, providerModels)
	}
}

func catalogModelDisplayName(models []map[string]any, modelID string) string {
	for _, model := range models {
		if model != nil && model["id"] == modelID {
			name, _ := model["display_name"].(string)
			return name
		}
	}
	return ""
}

func modelInfoDisplayName(models []*ModelInfo, modelID string) string {
	for _, model := range models {
		if model != nil && model.ID == modelID {
			return model.DisplayName
		}
	}
	return ""
}

func assertSelectedCatalogMetadata(t *testing.T, model *ModelInfo, wantOwner, wantDisplayName string, wantContextLength, wantMaxContextLength int) {
	t.Helper()

	if model == nil {
		t.Fatal("selected catalog model is nil")
	}
	if model.OwnedBy != wantOwner || model.DisplayName != wantDisplayName || model.ContextLength != wantContextLength || model.MaxContextLength != wantMaxContextLength {
		t.Fatalf("selected catalog metadata = %+v, want owner=%q display_name=%q context_length=%d max_context_length=%d", model, wantOwner, wantDisplayName, wantContextLength, wantMaxContextLength)
	}
}

func assertSelectedCatalogModelMap(t *testing.T, models []map[string]any, modelID, wantOwner, wantDisplayName string, wantContextLength, wantMaxContextLength int) {
	t.Helper()

	for _, model := range models {
		if model == nil || model["id"] != modelID {
			continue
		}
		if model["owned_by"] != wantOwner || model["display_name"] != wantDisplayName || model["context_length"] != wantContextLength || model["max_context_length"] != wantMaxContextLength {
			t.Fatalf("selected catalog map = %#v, want owner=%q display_name=%q context_length=%d max_context_length=%d", model, wantOwner, wantDisplayName, wantContextLength, wantMaxContextLength)
		}
		return
	}

	t.Fatalf("catalog does not contain model %q: %#v", modelID, models)
}
