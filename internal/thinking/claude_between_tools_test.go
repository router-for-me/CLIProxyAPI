package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

// claude-sonnet-5-5 rejects thinking.type="disabled" and asks for "between_tools"
// instead. That type takes no other field and is accepted only at effort high or
// below, so turning thinking off must send exactly {"type":"between_tools"} and
// leave effort to the model default.
func TestApplyThinking_ClaudeSonnet55DisabledUsesBetweenTools(t *testing.T) {
	const msgs = `"messages":[{"role":"user","content":"hi"}]`
	tests := []struct {
		name  string
		body  string
		model string
	}{
		{name: "disabled", body: `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"disabled"}}`, model: "claude-sonnet-5-5"},
		{name: "disabled with stale fields", body: `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"disabled","budget_tokens":8000,"display":"omitted"},"output_config":{"effort":"xhigh"}}`, model: "claude-sonnet-5-5"},
		{name: "suffix none", body: `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, model: "claude-sonnet-5-5(none)"},
		{name: "zero budget", body: `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"enabled","budget_tokens":0}}`, model: "claude-sonnet-5-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), tt.model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking").Raw; got != `{"type":"between_tools"}` {
				t.Fatalf("thinking = %s, want {\"type\":\"between_tools\"}; body=%s", got, out)
			}
			if gjson.GetBytes(out, "output_config.effort").Exists() {
				t.Fatalf("between_tools keeps output_config.effort: %s", out)
			}
		})
	}
}

// Only the turn-off value changes; other levels and models keep their behaviour.
func TestApplyThinking_ClaudeBetweenToolsLimitedToSonnet55Off(t *testing.T) {
	const msgs = `"messages":[{"role":"user","content":"hi"}]`
	out, err := thinking.ApplyThinking([]byte(`{"model":"claude-sonnet-5-5",`+msgs+`,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`), "claude-sonnet-5-5", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking: %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("sonnet-5-5 high: thinking.type = %q, want adaptive; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("sonnet-5-5 high: effort = %q, want high; body=%s", got, out)
	}

	for _, model := range []string{"claude-sonnet-5", "claude-opus-5"} {
		out, err := thinking.ApplyThinking([]byte(`{"model":"`+model+`",`+msgs+`,"thinking":{"type":"disabled"}}`), model, "claude", "claude", "claude")
		if err != nil {
			t.Fatalf("%s ApplyThinking: %v", model, err)
		}
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
			t.Fatalf("%s: thinking.type = %q, want disabled; body=%s", model, got, out)
		}
	}
}
