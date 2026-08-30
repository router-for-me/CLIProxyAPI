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
