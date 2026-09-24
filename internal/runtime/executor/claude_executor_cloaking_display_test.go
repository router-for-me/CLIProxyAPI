package executor

import (
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestReconcileClaudeCloakDisplayAfterThinkingOverride(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5", "claude-fable-5-1"} {
		t.Run(model, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				thinking string
				injected bool
				touched  bool
				want     string
			}{
				{name: "disabled removes synthetic display", thinking: `{"type":"disabled","display":"updates"}`, injected: true},
				{name: "missing type removes synthetic display", thinking: `{"display":"updates"}`, injected: true},
				{name: "adaptive keeps synthetic display", thinking: `{"type":"adaptive","display":"updates"}`, injected: true, want: "updates"},
				{name: "enabled keeps synthetic display", thinking: `{"type":"enabled","budget_tokens":2048,"display":"updates"}`, injected: true, want: "updates"},
				{name: "caller display stays caller owned", thinking: `{"type":"disabled","display":"summarized"}`, want: "summarized"},
				{name: "payload display stays operator owned", thinking: `{"type":"disabled","display":"omitted"}`, injected: true, touched: true, want: "omitted"},
				{name: "payload deletion is not refilled", thinking: `{"type":"adaptive"}`, injected: true, touched: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body, _ := sjson.SetBytes([]byte(`{}`), "model", model)
					body, _ = sjson.SetRawBytes(body, "thinking", []byte(tc.thinking))
					out := reconcileClaudeCodeFableModelAfterPayload(body, claudeCodeFableState{injectedDisplay: tc.injected}, false, tc.touched, true, false)
					display := gjson.GetBytes(out, "thinking.display")
					if display.String() != tc.want || (tc.want == "" && display.Exists()) {
						t.Fatalf("display=%s, want %q; body=%s", display.Raw, tc.want, out)
					}
				})
			}
		})
	}
}

func TestReconcileClaudeCloakDisplayRemovesSyntheticDisplayForNonProgressModel(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","thinking":{"type":"adaptive","display":"updates"}}`)
	out := reconcileClaudeCodeFableModelAfterPayload(body, claudeCodeFableState{injectedDisplay: true}, false, false, true, false)
	if display := gjson.GetBytes(out, "thinking.display"); display.Exists() {
		t.Fatalf("synthetic display survived for non-progress model: %s", out)
	}
}
