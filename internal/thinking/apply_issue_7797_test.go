package thinking_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
)

func TestApplyThinking_AntigravityClaudeStrippedBudgetOmitsOrphanIncludeThoughts(t *testing.T) {
	t.Run("registered_model_maxOutputTokens_clamps_below_min", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":1024,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinking(body, "claude-sonnet-4-6", "antigravity", "antigravity", "antigravity")
		if errApply != nil {
			t.Fatalf("ApplyThinking() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent after maxOutputTokens clamping below Min, got %s in body=%s", tcNode.Raw, out)
		}
	})

	t.Run("registered_model_suffix_none_with_includeThoughts_true", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinking(body, "claude-sonnet-4-6(none)", "antigravity", "antigravity", "antigravity")
		if errApply != nil {
			t.Fatalf("ApplyThinking() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent when disabled via (none) suffix, got %s in body=%s", tcNode.Raw, out)
		}
	})

	t.Run("registered_model_cross_protocol_summary_enabled_and_maxOutputTokens_below_min", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":512,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinkingWithSummary(
			body,
			"claude-sonnet-4-6",
			"openai",
			"antigravity",
			"antigravity",
			thinking.SummaryConfig{Mode: thinking.SummaryEnabled, Detail: "auto"},
		)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithSummary() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent when summary intent is enabled but Claude budget is stripped, got %s in body=%s", tcNode.Raw, out)
		}
	})

	t.Run("user_defined_claude_model_budget_below_min_with_summary_enabled", func(t *testing.T) {
		modelInfo := &registry.ModelInfo{
			ID:                  "custom-claude-sonnet-4-6",
			Type:                "antigravity",
			UserDefined:         true,
			MaxCompletionTokens: 64000,
			Thinking: &registry.ThinkingSupport{
				Min:            1024,
				Max:            64000,
				ZeroAllowed:    true,
				DynamicAllowed: true,
			},
		}
		body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":512}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, "custom-claude-sonnet-4-6", "antigravity", "antigravity", "antigravity", modelInfo)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent for user-defined Claude model with budget < Min, got %s in body=%s", tcNode.Raw, out)
		}
	})

	t.Run("in_place_doc_maxOutputTokens_clamps_below_min", func(t *testing.T) {
		doc := ojson.NewDoc([]byte(`{"request":{"generationConfig":{"maxOutputTokens":1024,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`))
		if errApply := thinking.ApplyThinkingDoc(doc, "claude-sonnet-4-6", "antigravity", "antigravity", "antigravity"); errApply != nil {
			t.Fatalf("ApplyThinkingDoc() error = %v", errApply)
		}
		if tcNode := doc.Get("request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent in doc after maxOutputTokens clamping below Min, got %s in body=%s", tcNode.Raw, doc.String())
		}
	})

	t.Run("registered_model_suffix_clamped_below_min_strips_snake_case_thinking_config_include_thoughts", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":1024,"thinking_config":{"include_thoughts":true}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinking(body, "claude-sonnet-4-6(2048)", "antigravity", "antigravity", "antigravity")
		if errApply != nil {
			t.Fatalf("ApplyThinking() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent, got %s in body=%s", tcNode.Raw, out)
		}
		if tcSnake := ojson.GetBytes(out, "request.generationConfig.thinking_config"); tcSnake.Exists() {
			t.Fatalf("expected thinking_config to be absent, got %s in body=%s", tcSnake.Raw, out)
		}
	})

	t.Run("user_defined_claude_model_suffix_clamped_below_min_strips_snake_case_thinking_config_includeThoughts", func(t *testing.T) {
		modelInfo := &registry.ModelInfo{
			ID:                  "custom-claude-sonnet-4-6",
			Type:                "antigravity",
			UserDefined:         true,
			MaxCompletionTokens: 64000,
			Thinking: &registry.ThinkingSupport{
				Min:            1024,
				Max:            64000,
				ZeroAllowed:    true,
				DynamicAllowed: true,
			},
		}
		body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":1024,"thinking_config":{"includeThoughts":true}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, "custom-claude-sonnet-4-6(2048)", "antigravity", "antigravity", "antigravity", modelInfo)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be absent, got %s in body=%s", tcNode.Raw, out)
		}
		if tcSnake := ojson.GetBytes(out, "request.generationConfig.thinking_config"); tcSnake.Exists() {
			t.Fatalf("expected thinking_config to be absent, got %s in body=%s", tcSnake.Raw, out)
		}
	})

	t.Run("registered_model_valid_clamped_budget_preserves_includeThoughts", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":2048,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		out, errApply := thinking.ApplyThinking(body, "claude-sonnet-4-6", "antigravity", "antigravity", "antigravity")
		if errApply != nil {
			t.Fatalf("ApplyThinking() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2047 {
			t.Fatalf("thinkingBudget = %d, want 2047; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
		}
	})
}
