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

<<<<<<< HEAD
func TestApplier_ClaudeModeNone_DoesNotInjectThinkingConfig(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID: "claude-opus-4-5-thinking",
		Thinking: &registry.ThinkingSupport{
			Min: 1024,
			Max: 32000,
		},
		MaxCompletionTokens: 64000,
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeNone,
		Budget: 0,
	}

	out, err := applier.Apply([]byte(`{"request":{"generationConfig":{}}}`), cfg, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("expected no thinkingConfig injection for mode none")
=======
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
>>>>>>> archive/pr-234-head-20260223
	}
}

func TestApplier_ClaudeBudgetBelowMin_RemovesThinkingConfigForNonNoneModes(t *testing.T) {
<<<<<<< HEAD
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID: "claude-opus-4-5-thinking",
		Thinking: &registry.ThinkingSupport{
			Min: 1024,
			Max: 32000,
		},
		MaxCompletionTokens: 64000,
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 512,
	}

	out, err := applier.Apply([]byte(`{"request":{"generationConfig":{}}}`), cfg, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if gjson.GetBytes(out, "request.generationConfig.thinkingConfig").Exists() {
		t.Fatalf("expected thinkingConfig to be removed when budget below min in non-none mode")
	}
}

func TestApplier_ClaudeBudgetAboveMaxCapsToMaxMinusOne(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-5-thinking",
		MaxCompletionTokens: 4000,
		Thinking: &registry.ThinkingSupport{
			Min: 1024,
			Max: 32000,
		},
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 4096,
	}

	out, err := applier.Apply([]byte(`{"request":{"generationConfig":{}}}`), cfg, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int()
	if got != 3999 {
		t.Fatalf("thinkingBudget=%d, want 3999", got)
	}
}

func TestApplier_ClaudeModeBudgetAddsDefaultMaxOutputTokens(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-5-thinking",
		MaxCompletionTokens: 5000,
		Thinking: &registry.ThinkingSupport{
			Min: 1024,
			Max: 32000,
		},
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 2048,
	}

	out, err := applier.Apply([]byte(`{"request":{"generationConfig":{}}}`), cfg, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 5000 {
		t.Fatalf("maxOutputTokens=%d, want 5000", got)
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
		t.Fatalf("thinkingBudget=%d, want 2048", got)
	}
}

func TestApplier_ClaudeBudgetCapRespectsExistingMaxOutputTokens(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-opus-4-5-thinking",
		MaxCompletionTokens: 9999,
		Thinking: &registry.ThinkingSupport{
			Min: 1024,
			Max: 32000,
		},
	}
	cfg := thinking.ThinkingConfig{
		Mode:   thinking.ModeBudget,
		Budget: 4096,
	}
	payload := `{"request":{"generationConfig":{"maxOutputTokens":3500}}}`

	out, err := applier.Apply([]byte(payload), cfg, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int()
	if got != 3499 {
		t.Fatalf("thinkingBudget=%d, want 3499", got)
	}
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 3500 {
		t.Fatalf("maxOutputTokens should remain user-provided: got %d", got)
=======
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
>>>>>>> archive/pr-234-head-20260223
	}
}
