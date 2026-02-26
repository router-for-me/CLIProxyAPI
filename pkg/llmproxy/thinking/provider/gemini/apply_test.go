package gemini

import (
	"testing"

	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/registry"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/thinking"
	"github.com/tidwall/gjson"
)

func TestApplyLevelFormatPreservesExplicitSnakeCaseIncludeThoughts(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"generationConfig":{"thinkingConfig":{"include_thoughts":false,"thinkingBudget":1024}}}`)
	cfg := thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}
	model := &registry.ModelInfo{ID: "gemini-3-flash", Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}}}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := gjson.ParseBytes(out)
	if !res.Get("generationConfig.thinkingConfig.thinkingLevel").Exists() {
		t.Fatalf("expected thinkingLevel to be set")
	}
	if res.Get("generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("expected includeThoughts=false from explicit include_thoughts")
	}
	if res.Get("generationConfig.thinkingConfig.include_thoughts").Exists() {
		t.Fatalf("expected include_thoughts to be normalized away")
	}
}

func TestApplyBudgetFormatModeNoneForcesIncludeThoughtsFalse(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`)
	cfg := thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0}
	model := &registry.ModelInfo{ID: "gemini-2.5-flash", Thinking: &registry.ThinkingSupport{Min: 0, Max: 24576, ZeroAllowed: true}}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res := gjson.ParseBytes(out)
	if res.Get("generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("expected includeThoughts=false for ModeNone")
	}
	if res.Get("generationConfig.thinkingConfig.thinkingBudget").Int() != 0 {
		t.Fatalf("expected thinkingBudget=0, got %d", res.Get("generationConfig.thinkingConfig.thinkingBudget").Int())
	}
}
