package thinking_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/openai"
)

func TestApplyThinking_NonThinkingModelStripsBlankAndNullEffortFields(t *testing.T) {
	t.Run("codex_strips_blank_effort_and_preserves_other_reasoning_fields", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "gpt-no-thinking", Type: "codex"}
		body := []byte(`{"reasoning":{"effort":"   ","other":7}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "codex", "codex", "codex", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if ojson.GetBytes(out, "reasoning.effort").Exists() {
			t.Fatalf("expected reasoning.effort to be stripped; body=%s", out)
		}
		if got := ojson.GetBytes(out, "reasoning.other").Int(); got != 7 {
			t.Fatalf("reasoning.other = %d, want 7; body=%s", got, out)
		}
	})

	t.Run("codex_strips_null_effort_and_drops_empty_reasoning", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "gpt-no-thinking", Type: "codex"}
		body := []byte(`{"reasoning":{"effort":null},"input":"hi"}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "codex", "codex", "codex", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if ojson.GetBytes(out, "reasoning").Exists() {
			t.Fatalf("expected empty reasoning object to be stripped; body=%s", out)
		}
		if got := ojson.GetBytes(out, "input").String(); got != "hi" {
			t.Fatalf("input = %q, want hi; body=%s", got, out)
		}
	})

	t.Run("openai_strips_null_and_blank_reasoning_effort", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "gpt-no-thinking", Type: "openai"}
		for _, raw := range []string{
			`{"reasoning_effort":null,"messages":[]}`,
			`{"reasoning_effort":"   ","messages":[]}`,
		} {
			body := []byte(raw)
			out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "openai", "openai", "openai", model)
			if errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfo(%s) error = %v", raw, errApply)
			}
			if ojson.GetBytes(out, "reasoning_effort").Exists() {
				t.Fatalf("expected reasoning_effort to be stripped for %s; body=%s", raw, out)
			}
		}
	})

	t.Run("claude_strips_null_budget_thinking_object", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "claude-no-thinking", Type: "claude"}
		body := []byte(`{"thinking":{"budget_tokens":null},"messages":[]}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "claude", "claude", "claude", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if ojson.GetBytes(out, "thinking").Exists() {
			t.Fatalf("expected thinking to be stripped; body=%s", out)
		}
	})
}

func TestApplyThinking_UserDefinedModelPreservesCustomLevelCasing(t *testing.T) {
	t.Run("openai_user_defined_preserves_custom_vendor_level", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "custom-openai-vendor", Type: "openai", UserDefined: true}
		body := []byte(`{"reasoning_effort":"VendorFast","messages":[]}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "openai", "openai", "openai", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "reasoning_effort").String(); got != "VendorFast" {
			t.Fatalf("reasoning_effort = %q, want VendorFast; body=%s", got, out)
		}
	})

	t.Run("codex_user_defined_preserves_custom_vendor_level", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "custom-codex-vendor", Type: "codex", UserDefined: true}
		body := []byte(`{"reasoning":{"effort":"VendorFast"},"input":"hi"}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "codex", "codex", "codex", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "reasoning.effort").String(); got != "VendorFast" {
			t.Fatalf("reasoning.effort = %q, want VendorFast; body=%s", got, out)
		}
	})

	t.Run("gemini_user_defined_preserves_custom_vendor_level", func(t *testing.T) {
		model := &registry.ModelInfo{ID: "custom-gemini-vendor", Type: "gemini", UserDefined: true}
		body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"VendorFast"}}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "gemini", "gemini", "gemini", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "VendorFast" {
			t.Fatalf("thinkingLevel = %q, want VendorFast; body=%s", got, out)
		}
	})
}

func TestApplyThinking_NullAndCaseInsensitiveEndToEnd(t *testing.T) {
	t.Run("codex_null_and_blank_effort_passthrough_without_400", func(t *testing.T) {
		model := &registry.ModelInfo{
			ID:   "gpt-codex-level",
			Type: "codex",
			Thinking: &registry.ThinkingSupport{
				ZeroAllowed:    true,
				DynamicAllowed: true,
				Levels:         []string{"low", "medium", "high"},
			},
		}
		for _, raw := range []string{
			`{"reasoning":{"effort":null},"input":"hi"}`,
			`{"reasoning":{"effort":"   "},"input":"hi"}`,
		} {
			body := []byte(raw)
			if _, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "codex", "codex", "codex", model); errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfo(%s) unexpected error = %v", raw, errApply)
			}
		}
	})

	t.Run("claude_enabled_with_null_budget_does_not_disable_thinking", func(t *testing.T) {
		model := &registry.ModelInfo{
			ID:   "claude-budget-model",
			Type: "claude",
			Thinking: &registry.ThinkingSupport{
				Min:            1024,
				Max:            32768,
				ZeroAllowed:    true,
				DynamicAllowed: true,
			},
		}
		body := []byte(`{"thinking":{"type":"enabled","budget_tokens":null},"messages":[]}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "claude", "claude", "claude", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "thinking.type").String(); got != "enabled" {
			t.Fatalf("thinking.type = %q, want enabled; body=%s", got, out)
		}
	})
}
