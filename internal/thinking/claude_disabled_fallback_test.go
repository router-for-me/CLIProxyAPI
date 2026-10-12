package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

// Anthropic rejects thinking.type="disabled" on claude-opus-5-5, claude-fable-5 and
// claude-fable-5-1 ("Use thinking.type.adaptive and output_config.effort"). Turning
// thinking off must fall back to adaptive thinking at the lowest level there, and keep
// the explicit disabled block on models that accept it.
func TestApplyThinking_ClaudeDisabledFallsBackOnAdaptiveOnlyModels(t *testing.T) {
	tests := []struct {
		model        string
		wantThinking string
		wantEffort   string
	}{
		{model: "claude-opus-5-5", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-fable-5", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-fable-5-1", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-opus-5", wantThinking: "disabled"},
		{model: "claude-opus-4-8", wantThinking: "disabled"},
		{model: "claude-haiku-4-5-20251001", wantThinking: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			body := `{"model":"` + tt.model + `","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`
			out, err := thinking.ApplyThinking([]byte(body), tt.model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != tt.wantThinking {
				t.Fatalf("thinking.type = %q, want %q; body=%s", got, tt.wantThinking, out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != tt.wantEffort {
				t.Fatalf("output_config.effort = %q, want %q; body=%s", got, tt.wantEffort, out)
			}
			if tt.wantThinking == "disabled" && gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("disabled thinking carries budget_tokens: %s", out)
			}
		})
	}
}

// Every way of turning thinking off must reach the same fallback, and the fallback
// must not keep a manual budget from the incoming body.
func TestApplyThinking_ClaudeDisabledFallbackInputs(t *testing.T) {
	const manual = `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8000}}`
	tests := []struct {
		name  string
		body  string
		model string
	}{
		{name: "suffix none", body: manual, model: "claude-opus-5-5(none)"},
		{name: "suffix zero", body: manual, model: "claude-opus-5-5(0)"},
		{name: "zero budget", body: `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0}}`, model: "claude-opus-5-5"},
		// Translated OpenAI reasoning_effort "none" arrives as thinking.type="disabled".
		{name: "disabled with stale fields", body: `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled","budget_tokens":8000}}`, model: "claude-opus-5-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), tt.model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "low" {
				t.Fatalf("output_config.effort = %q, want low; body=%s", got, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("fallback keeps budget_tokens: %s", out)
			}
		})
	}
}
