package registry

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEmbeddedClaudeHaiku55(t *testing.T) {
	var catalog staticModelsJSON
	if err := json.Unmarshal(embeddedModelsJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Claude {
		if model.ID != "claude-haiku-5-5" {
			continue
		}
		if model.ContextLength != 1000000 || model.MaxCompletionTokens != 128000 {
			t.Fatalf("incorrect limits: %+v", model)
		}
		if model.Thinking == nil || !model.Thinking.DynamicAllowed || !model.Thinking.ZeroAllowed || !reflect.DeepEqual(model.Thinking.Levels, []string{"low", "medium", "high", "xhigh", "max"}) {
			t.Fatalf("incorrect thinking: %+v", model.Thinking)
		}
		return
	}
	t.Fatal("Haiku 5.5 absent from embedded Claude catalog")
}
