package thinking_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
)

func TestApplyThinking_UserDefinedNativeGeminiPreservesThinkingLevel(t *testing.T) {
	for _, tc := range []struct {
		level           string
		wantAntigravity string
	}{
		{level: "medium", wantAntigravity: "MEDIUM"},
		{level: "high", wantAntigravity: "HIGH"},
		{level: "xhigh", wantAntigravity: "XHIGH"},
	} {
		level := tc.level
		t.Run("user_defined_gemini_to_gemini_"+level, func(t *testing.T) {
			modelInfo := &registry.ModelInfo{
				ID:          "custom-gemini-4-model",
				Type:        "gemini",
				UserDefined: true,
			}
			body := []byte(`{"generationConfig":{"maxOutputTokens":1030323,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, "custom-gemini-4-model", "gemini", "gemini", "vertex", modelInfo)
			if errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
			}
			if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != level {
				t.Fatalf("thinkingLevel = %q, want %q; body=%s", got, level, out)
			}
			if ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Fatalf("unexpected thinkingBudget in native Gemini payload: %s", out)
			}
			if !ojson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts").Bool() {
				t.Fatalf("expected includeThoughts=true preserved: %s", out)
			}
		})

		t.Run("unknown_model_gemini_to_gemini_"+level, func(t *testing.T) {
			body := []byte(`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			out, errApply := thinking.ApplyThinking(body, "unknown-gemini-4-preview", "gemini", "gemini", "gemini")
			if errApply != nil {
				t.Fatalf("ApplyThinking() error = %v", errApply)
			}
			if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != level {
				t.Fatalf("thinkingLevel = %q, want %q; body=%s", got, level, out)
			}
			if ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Fatalf("unexpected thinkingBudget in native Gemini payload: %s", out)
			}
		})

		t.Run("unknown_model_gemini_to_antigravity_"+level, func(t *testing.T) {
			body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
			out, errApply := thinking.ApplyThinking(body, "unknown-antigravity-gemini-4", "gemini", "antigravity", "antigravity")
			if errApply != nil {
				t.Fatalf("ApplyThinking() error = %v", errApply)
			}
			if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != tc.wantAntigravity {
				t.Fatalf("thinkingLevel = %q, want %q; body=%s", got, tc.wantAntigravity, out)
			}
			if ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Fatalf("unexpected thinkingBudget in Antigravity payload: %s", out)
			}
		})

		t.Run("unknown_model_interactions_to_gemini_"+level, func(t *testing.T) {
			body := []byte(`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			out, errApply := thinking.ApplyThinking(body, "unknown-interactions-gemini-4", "interactions", "gemini", "gemini")
			if errApply != nil {
				t.Fatalf("ApplyThinking() error = %v", errApply)
			}
			if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != level {
				t.Fatalf("thinkingLevel = %q, want %q; body=%s", got, level, out)
			}
			if ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Fatalf("unexpected thinkingBudget in Interactions->Gemini payload: %s", out)
			}
		})
	}
}

func TestApplyThinking_UserDefinedCrossProtocolAndSpecialLevelBehavior(t *testing.T) {
	t.Run("openai_to_claude_user_defined_clamps_translated_budget", func(t *testing.T) {
		modelInfo := &registry.ModelInfo{
			ID:          "custom-claude",
			Type:        "claude",
			UserDefined: true,
			Thinking:    &registry.ThinkingSupport{Min: 1024, Max: 20000},
		}
		body := []byte(`{"max_tokens":200000,"thinking":{"type":"enabled","budget_tokens":128000}}`)
		source := []byte(`{"reasoning_effort":"max"}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, source, modelInfo.ID, "openai", "claude", "claude", modelInfo)
		if errApply != nil {
			t.Fatal(errApply)
		}
		if got := ojson.GetBytes(out, "thinking.budget_tokens").Int(); got != 20000 {
			t.Fatalf("budget_tokens = %d, want 20000; body=%s", got, out)
		}
	})

	t.Run("openai_to_gemini_opaque_user_defined_converts_to_budget", func(t *testing.T) {
		modelInfo := &registry.ModelInfo{
			ID:          "opaque-custom",
			Type:        "gemini",
			UserDefined: true,
		}
		body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
		source := []byte(`{"reasoning_effort":"medium","messages":[{"role":"user","content":"hello"}]}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, source, "opaque-custom", "openai", "gemini", "gemini", modelInfo)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 8192 {
			t.Fatalf("thinkingBudget = %d, want 8192; body=%s", got, out)
		}
		if ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").Exists() {
			t.Fatalf("unexpected thinkingLevel on opaque cross-protocol conversion: %s", out)
		}
	})

	t.Run("openai_to_gemini_user_defined_with_levels_preserves_thinking_level", func(t *testing.T) {
		modelInfo := &registry.ModelInfo{
			ID:          "vertex-extra-model",
			Type:        "vertex",
			UserDefined: true,
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		}
		body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
		source := []byte(`{"reasoning_effort":"medium","messages":[{"role":"user","content":"hello"}]}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, source, "vertex-extra-model", "openai", "gemini", "vertex", modelInfo)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "medium" {
			t.Fatalf("thinkingLevel = %q, want medium; body=%s", got, out)
		}
		if ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
			t.Fatalf("unexpected thinkingBudget on level-capable user-defined model: %s", out)
		}
	})

	t.Run("native_gemini_uppercase_auto_still_maps_to_dynamic_budget", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"AUTO"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
		out, errApply := thinking.ApplyThinking(body, "unknown-gemini-model", "gemini", "gemini", "gemini")
		if errApply != nil {
			t.Fatalf("ApplyThinking() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != -1 {
			t.Fatalf("thinkingBudget = %d, want -1; body=%s", got, out)
		}
		if ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").Exists() {
			t.Fatalf("unexpected thinkingLevel=AUTO preserved: %s", out)
		}
	})
}
