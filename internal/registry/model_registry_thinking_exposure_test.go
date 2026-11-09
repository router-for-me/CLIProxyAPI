package registry

import "testing"

func TestConvertModelToMapOpenAIExposesThinkingSupport(t *testing.T) {
	modelRegistry := newTestModelRegistry()
	modelRegistry.RegisterClient("openai-client", "openai", []*ModelInfo{
		{
			ID:   "thinking-model",
			Type: "openai",
			Thinking: &ThinkingSupport{
				Levels: []string{"max", "xhigh", "high"},
			},
		},
		{ID: "plain-model", Type: "openai"},
	})

	models := modelRegistry.GetAvailableModels("openai")
	if len(models) != 2 {
		t.Fatalf("available model count = %d, want 2", len(models))
	}

	byID := map[string]map[string]any{}
	for _, model := range models {
		id, _ := model["id"].(string)
		byID[id] = model
	}

	thinking, ok := byID["thinking-model"]["thinking"].(*ThinkingSupport)
	if !ok {
		t.Fatalf("thinking metadata missing or wrong type: %#v", byID["thinking-model"]["thinking"])
	}
	if len(thinking.Levels) != 3 || thinking.Levels[0] != "max" || thinking.Levels[2] != "high" {
		t.Fatalf("thinking levels = %#v, want [max xhigh high]", thinking.Levels)
	}

	if _, exists := byID["plain-model"]["thinking"]; exists {
		t.Fatalf("plain model must not expose a thinking field: %#v", byID["plain-model"])
	}
}
