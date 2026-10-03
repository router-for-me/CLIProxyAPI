package thinking_test

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/xai"
	"github.com/tidwall/gjson"
)

func TestGrok3MiniReasoningLevelsClampMediumToLow(t *testing.T) {
	xaiModels := registry.GetXAIModels()
	modelsByID := make(map[string]*registry.ModelInfo)
	for _, m := range xaiModels {
		if m != nil {
			modelsByID[m.ID] = m
		}
	}

	modelIDs := []string{"grok-3-mini", "grok-3-mini-fast"}
	for _, modelID := range modelIDs {
		t.Run(modelID, func(t *testing.T) {
			modelInfo, ok := modelsByID[modelID]
			if !ok || modelInfo == nil {
				t.Fatalf("model %s not found in GetXAIModels()", modelID)
			}

			if modelInfo.Thinking == nil {
				t.Fatalf("model %s thinking is nil", modelID)
			}
			wantLevels := []string{"low", "high"}
			if !reflect.DeepEqual(modelInfo.Thinking.Levels, wantLevels) {
				t.Fatalf("model %s thinking.levels = %v, want %v", modelID, modelInfo.Thinking.Levels, wantLevels)
			}

			openaiSource := []byte(`{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"medium"}`)
			out, err := thinking.ApplyThinkingWithModelInfo([]byte(`{}`), openaiSource, modelID, "openai", "xai", "xai", modelInfo)
			if err != nil {
				t.Fatalf("openai -> xai with medium effort failed: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
				t.Fatalf("openai -> xai with medium effort: reasoning.effort = %q, want low; body=%s", got, out)
			}

			respSource := []byte(`{"input":"hello","reasoning":{"effort":"medium"}}`)
			out, err = thinking.ApplyThinkingWithModelInfo([]byte(`{}`), respSource, modelID, "openai-response", "xai", "xai", modelInfo)
			if err != nil {
				t.Fatalf("openai-response -> xai with medium effort failed: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
				t.Fatalf("openai-response -> xai with medium effort: reasoning.effort = %q, want low; body=%s", got, out)
			}

			openaiSourceHigh := []byte(`{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`)
			out, err = thinking.ApplyThinkingWithModelInfo([]byte(`{}`), openaiSourceHigh, modelID, "openai", "xai", "xai", modelInfo)
			if err != nil {
				t.Fatalf("openai -> xai with high effort failed: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
				t.Fatalf("openai -> xai with high effort: reasoning.effort = %q, want high; body=%s", got, out)
			}

			respSourceHigh := []byte(`{"input":"hello","reasoning":{"effort":"high"}}`)
			out, err = thinking.ApplyThinkingWithModelInfo([]byte(`{}`), respSourceHigh, modelID, "openai-response", "xai", "xai", modelInfo)
			if err != nil {
				t.Fatalf("openai-response -> xai with high effort failed: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
				t.Fatalf("openai-response -> xai with high effort: reasoning.effort = %q, want high; body=%s", got, out)
			}
		})
	}
}
