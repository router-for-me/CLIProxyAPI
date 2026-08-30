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
