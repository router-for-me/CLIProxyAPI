package antigravity

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

func TestApplier_ClaudeModeNone_PreservesDisableIntentUnderMinBudget(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`)
	cfg := thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0}
	model := &registry.ModelInfo{
		ID:                  "claude-sonnet-4-5",
		MaxCompletionTokens: 4096,
		Thinking:            &registry.ThinkingSupport{Min: 1024},
	}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := gjson.ParseBytes(out)
	if !res.Get("request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("expected thinkingConfig to remain for ModeNone")
	}
	if got := res.Get("request.generationConfig.thinkingConfig.includeThoughts").Bool(); got {
		t.Fatalf("expected includeThoughts=false for ModeNone")
	}
	if got := res.Get("request.generationConfig.thinkingConfig.thinkingBudget").Int(); got < 1024 {
		t.Fatalf("expected budget clamped to min >= 1024, got %d", got)
	}
}

func TestApplier_ClaudeBudgetBelowMin_RemovesThinkingConfigForNonNoneModes(t *testing.T) {
	a := NewApplier()
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`)
	cfg := thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 1}
	model := &registry.ModelInfo{
		ID:                  "claude-sonnet-4-5",
		MaxCompletionTokens: 4096,
		Thinking:            &registry.ThinkingSupport{Min: 1024},
	}

	out, err := a.Apply(body, cfg, model)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res := gjson.ParseBytes(out)
	if res.Get("request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("expected thinkingConfig removed for non-ModeNone min-budget violation")
	}
}
