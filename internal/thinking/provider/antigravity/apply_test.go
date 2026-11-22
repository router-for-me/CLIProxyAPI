package antigravity_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
)

func claudeSonnet46AntigravityModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:                  "claude-sonnet-4-6",
		Type:                "antigravity",
		MaxCompletionTokens: 64000,
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            64000,
			ZeroAllowed:    true,
			DynamicAllowed: true,
		},
	}
}

func TestApplier_ClaudeInvalidBudgetDoesNotRecreateOrphanIncludeThoughts(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}
	modelInfo := claudeSonnet46AntigravityModel()

	tests := []struct {
		name   string
		body   string
		config thinking.ThinkingConfig
	}{
		{
			name:   "budget_below_min_with_includeThoughts_true",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":512}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
		{
			name:   "budget_below_min_with_snake_case_include_thoughts_true",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"include_thoughts":true,"thinkingBudget":512}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
		{
			name:   "budget_below_min_with_includeThoughts_false",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":false,"thinkingBudget":512}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
		{
			name:   "maxOutputTokens_clamps_budget_below_min_with_includeThoughts_true",
			body:   `{"request":{"generationConfig":{"maxOutputTokens":1024,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048},
		},
		{
			name:   "mode_none_budget_zero_with_includeThoughts_true",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":0}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0},
		},
		{
			name:   "mode_none_budget_zero_with_includeThoughts_false",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":false,"thinkingBudget":0}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0},
		},
		{
			name:   "maxOutputTokens_clamps_below_min_with_snake_case_thinking_config_include_thoughts",
			body:   `{"request":{"generationConfig":{"maxOutputTokens":1024,"thinking_config":{"include_thoughts":true}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048},
		},
		{
			name:   "budget_below_min_with_snake_case_thinking_config_includeThoughts",
			body:   `{"request":{"generationConfig":{"thinking_config":{"includeThoughts":true}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errApply := applier.Apply([]byte(tc.body), tc.config, modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}
			if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
				t.Fatalf("expected thinkingConfig to be absent when Claude budget is stripped, got %s in body=%s", tcNode.Raw, out)
			}
			if tcSnake := ojson.GetBytes(out, "request.generationConfig.thinking_config"); tcSnake.Exists() {
				t.Fatalf("expected thinking_config to be absent when Claude budget is stripped, got %s in body=%s", tcSnake.Raw, out)
			}
		})
	}
}

func TestApplier_ClaudeValidBudgetPreservesIncludeThoughts(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}
	modelInfo := claudeSonnet46AntigravityModel()

	body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":2048,"thinkingConfig":{"include_thoughts":true,"thinkingBudget":4096}}}}`)
	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 4096}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2047 {
		t.Fatalf("thinkingBudget = %d, want 2047; body=%s", got, out)
	}
	if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
		t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
	}
	if ojson.GetBytes(out, "request.generationConfig.thinkingConfig.include_thoughts").Exists() {
		t.Fatalf("expected snake_case include_thoughts to be normalized; body=%s", out)
	}
}
