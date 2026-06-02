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

func TestGetMiniMaxModelsReturnsEmbeddedEntries(t *testing.T) {
	models := GetMiniMaxModels()
	if len(models) == 0 {
		t.Fatalf("expected MiniMax models to be populated from embedded catalog")
	}

	ids := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m == nil {
			continue
		}
		ids[m.ID] = struct{}{}
	}
	if _, ok := ids["MiniMax-M3"]; !ok {
		t.Fatalf("expected MiniMax-M3 to be the default MiniMax model")
	}
	if _, ok := ids["MiniMax-M2.7"]; !ok {
		t.Fatalf("expected MiniMax-M2.7 to be kept for backward compatibility")
	}
}

func TestGetStaticModelDefinitionsByChannelIncludesMiniMax(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("minimax")
	if len(models) == 0 {
		t.Fatalf("expected MiniMax channel to return model definitions")
	}
}

func TestLookupStaticModelInfoFindsMiniMax(t *testing.T) {
	info := LookupStaticModelInfo("MiniMax-M3")
	if info == nil {
		t.Fatalf("expected MiniMax-M3 to be discoverable via LookupStaticModelInfo")
	}
	if info.Type != "minimax" {
		t.Fatalf("expected MiniMax-M3 type to be minimax, got %q", info.Type)
	}
}
