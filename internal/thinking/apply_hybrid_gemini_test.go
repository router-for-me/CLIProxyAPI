package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/gemini"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingWithHybridGeminiModelPreservesNativeBudget(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:          "hybrid-gemini",
		Type:        "gemini",
		UserDefined: true,
		Thinking: &registry.ThinkingSupport{
			Min:    1024,
			Max:    32768,
			Levels: []string{"low", "medium", "high"},
		},
	}
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, body, modelInfo.ID, "gemini", "gemini", "gemini", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 4096 {
		t.Fatalf("thinkingBudget = %d, want 4096; body=%s", got, out)
	}
	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("hybrid Gemini budget was converted to thinkingLevel: %s", out)
	}
}

func TestApplyThinkingWithHybridGeminiModelConvertsNativeLevelToBudget(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:          "hybrid-gemini",
		Type:        "gemini",
		UserDefined: true,
		Thinking: &registry.ThinkingSupport{
			Min:    1024,
			Max:    32768,
			Levels: []string{"low", "medium", "high"},
		},
	}
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, body, modelInfo.ID, "gemini", "gemini", "gemini", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 24576 {
		t.Fatalf("thinkingBudget = %d, want 24576; body=%s", got, out)
	}
	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("hybrid Gemini level was not converted to thinkingBudget: %s", out)
	}
}
