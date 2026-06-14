package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/gemini"
	"github.com/tidwall/gjson"
)

// TestApplyThinking_DefaultWhenNoConfig ensures that when a model supports thinking
// but the request body does not include any thinking configuration, ApplyThinking
// injects a sensible default so that thought/reasoning content is returned.
func TestApplyThinking_DefaultWhenNoConfig(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-default-thinking-" + t.Name()

	// Register a budget-based thinking model (Gemini 2.5 style)
	budgetModelID := "test-gemini-2.5-flash"
	reg.RegisterClient(clientID, "gemini", []*registry.ModelInfo{{
		ID: budgetModelID,
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            24576,
			DynamicAllowed: true,
		},
	}})

	// Register a level-based thinking model (Gemini 3.x style)
	levelModelID := "test-gemini-3-pro"
	reg.RegisterClient(clientID+"-level", "gemini", []*registry.ModelInfo{{
		ID: levelModelID,
		Thinking: &registry.ThinkingSupport{
			Min:    0,
			Max:    0,
			Levels: []string{"low", "medium", "high"},
		},
	}})

	// Register a model without thinking support
	noThinkingModelID := "test-gemini-no-thinking"
	reg.RegisterClient(clientID+"-no-thinking", "gemini", []*registry.ModelInfo{{
		ID:       noThinkingModelID,
		Thinking: nil,
	}})

	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
		reg.UnregisterClient(clientID + "-level")
		reg.UnregisterClient(clientID + "-no-thinking")
	})

	t.Run("budget_model_no_config_gets_auto_default", func(t *testing.T) {
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}`)
		out, err := thinking.ApplyThinking(body, budgetModelID, "openai", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking() error = %v", err)
		}

		t.Logf("Output: %s", string(out))

		// Should have thinkingBudget=-1 (auto) and includeThoughts=true
		budget := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget")
		if !budget.Exists() {
			t.Fatal("thinkingConfig.thinkingBudget should exist in output")
		}
		if budget.Int() != -1 {
			t.Fatalf("thinkingBudget = %d, want -1 (auto)", budget.Int())
		}

		includeThoughts := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts")
		if !includeThoughts.Exists() {
			t.Fatal("thinkingConfig.includeThoughts should exist in output")
		}
		if !includeThoughts.Bool() {
			t.Fatal("includeThoughts = false, want true")
		}
	})

	t.Run("level_model_no_config_gets_lowest_level", func(t *testing.T) {
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}`)
		out, err := thinking.ApplyThinking(body, levelModelID, "openai", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking() error = %v", err)
		}

		t.Logf("Output: %s", string(out))

		// Should have thinkingLevel=low (first/lowest level) and includeThoughts=true
		level := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel")
		if !level.Exists() {
			t.Fatal("thinkingConfig.thinkingLevel should exist in output")
		}
		if level.String() != "low" {
			t.Fatalf("thinkingLevel = %q, want %q", level.String(), "low")
		}

		includeThoughts := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts")
		if !includeThoughts.Exists() {
			t.Fatal("thinkingConfig.includeThoughts should exist in output")
		}
		if !includeThoughts.Bool() {
			t.Fatal("includeThoughts = false, want true")
		}
	})

	t.Run("no_thinking_model_no_config_still_passthrough", func(t *testing.T) {
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}`)
		out, err := thinking.ApplyThinking(body, noThinkingModelID, "openai", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking() error = %v", err)
		}

		// Should NOT have thinkingConfig since model doesn't support thinking
		tc := gjson.GetBytes(out, "generationConfig.thinkingConfig")
		if tc.Exists() {
			t.Fatalf("thinkingConfig should NOT exist for non-thinking model, got: %s", tc.Raw)
		}
	})

	t.Run("explicit_config_overrides_default", func(t *testing.T) {
		// When the translator already set thinkingLevel=high, ApplyThinking should pick that up
		// (the body is in Gemini format after translation from OpenAI)
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}}}`)
		out, err := thinking.ApplyThinking(body, levelModelID, "gemini", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking() error = %v", err)
		}

		t.Logf("Output: %s", string(out))

		level := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel")
		if !level.Exists() {
			t.Fatal("thinkingConfig.thinkingLevel should exist in output")
		}
		if level.String() != "high" {
			t.Fatalf("thinkingLevel = %q, want %q (explicit)", level.String(), "high")
		}

		includeThoughts := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts")
		if !includeThoughts.Exists() {
			t.Fatal("thinkingConfig.includeThoughts should exist")
		}
		if !includeThoughts.Bool() {
			t.Fatal("includeThoughts = false, want true")
		}
	})

	t.Run("explicit_none_disables_thinking", func(t *testing.T) {
		// When translated body has includeThoughts=false (reasoning_effort=none → thinkingLevel=none)
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"none","includeThoughts":false}}}`)
		out, err := thinking.ApplyThinking(body, levelModelID, "gemini", "gemini", "gemini")
		if err != nil {
			t.Fatalf("ApplyThinking() error = %v", err)
		}

		t.Logf("Output: %s", string(out))

		includeThoughts := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts")
		if !includeThoughts.Exists() {
			t.Fatal("thinkingConfig.includeThoughts should exist")
		}
		if includeThoughts.Bool() {
			t.Fatal("includeThoughts = true, want false (user explicitly set none)")
		}
	})
}
