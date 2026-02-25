package geminicli

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
	"github.com/tidwall/gjson"
)

func TestApplyLevelFormatPreservesExplicitSnakeCaseIncludeThoughts(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"include_thoughts":false,"thinkingBudget":1024}}}}`)
	cfg := thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}
	model := &registry.ModelInfo{ID: "gemini-3-flash", Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}}}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := gjson.ParseBytes(out)
	if !res.Get("request.generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("expected thinkingLevel to be set")
	}
	if res.Get("request.generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("expected includeThoughts=false from explicit include_thoughts")
	}
	if res.Get("request.generationConfig.thinkingConfig.include_thoughts").Exists() {
		t.Fatalf("expected include_thoughts to be normalized away")
	}
}
