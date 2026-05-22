package registry

import "testing"

func TestGetDeepSeekModels(t *testing.T) {
	models := GetDeepSeekModels()
	if len(models) == 0 {
		t.Fatal("GetDeepSeekModels() returned no models")
	}
	found := false
	for _, model := range models {
		if model != nil && model.ID == "deepseek-v4-flash" {
			found = true
			if model.Type != "deepseek" {
				t.Fatalf("Type = %q", model.Type)
			}
			if model.Thinking == nil {
				t.Fatal("Thinking support is nil")
			}
		}
	}
	if !found {
		t.Fatal("deepseek-v4-flash not found")
	}
	if got := GetStaticModelDefinitionsByChannel("deepseek"); len(got) == 0 {
		t.Fatal("GetStaticModelDefinitionsByChannel(deepseek) returned no models")
	}
	if got := LookupStaticModelInfo("deepseek-v4-pro"); got == nil {
		t.Fatal("LookupStaticModelInfo(deepseek-v4-pro) returned nil")
	}
}
