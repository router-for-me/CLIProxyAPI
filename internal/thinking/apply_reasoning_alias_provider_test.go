package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_ReasoningAliasSuffixAppliesProviderSide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		model       string
		fromFormat  string
		toFormat    string
		providerKey string
		wantPath    string
		wantValue   string
		absentPath  string
	}{
		{
			name:        "codex gpt 5.5 high sets reasoning effort",
			model:       "gpt-5.5(high)",
			fromFormat:  "openai",
			toFormat:    "codex",
			providerKey: "codex",
			wantPath:    "reasoning.effort",
			wantValue:   "high",
		},
		{
			name:        "codex gpt 5.4 mini xhigh sets reasoning effort",
			model:       "gpt-5.4-mini(xhigh)",
			fromFormat:  "openai",
			toFormat:    "codex",
			providerKey: "codex",
			wantPath:    "reasoning.effort",
			wantValue:   "xhigh",
		},
		{
			name:        "claude 4.5 xhigh maps to thinking budget",
			model:       "claude-sonnet-4-5-20250929(xhigh)",
			fromFormat:  "openai",
			toFormat:    "claude",
			providerKey: "claude",
			wantPath:    "thinking.type",
			wantValue:   "enabled",
			absentPath:  "output_config.effort",
		},
		{
			name:        "claude 4.6 high sets adaptive effort",
			model:       "claude-sonnet-4-6(high)",
			fromFormat:  "openai",
			toFormat:    "claude",
			providerKey: "claude",
			wantPath:    "output_config.effort",
			wantValue:   "high",
			absentPath:  "thinking.budget_tokens",
		},
		{
			name:        "claude 4.7 xhigh sets adaptive effort",
			model:       "claude-opus-4-7(xhigh)",
			fromFormat:  "openai",
			toFormat:    "claude",
			providerKey: "claude",
			wantPath:    "output_config.effort",
			wantValue:   "xhigh",
			absentPath:  "thinking.budget_tokens",
		},
		{
			name:        "claude 4.7 max sets adaptive effort",
			model:       "claude-opus-4-7(max)",
			fromFormat:  "openai",
			toFormat:    "claude",
			providerKey: "claude",
			wantPath:    "output_config.effort",
			wantValue:   "max",
			absentPath:  "thinking.budget_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := thinking.ApplyThinking([]byte(`{}`), tt.model, tt.fromFormat, tt.toFormat, tt.providerKey)
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v", err)
			}
			if got := gjson.GetBytes(out, tt.wantPath).String(); got != tt.wantValue {
				t.Fatalf("%s = %q, want %q, body=%s", tt.wantPath, got, tt.wantValue, string(out))
			}
			if tt.absentPath != "" && gjson.GetBytes(out, tt.absentPath).Exists() {
				t.Fatalf("%s should be absent, body=%s", tt.absentPath, string(out))
			}
		})
	}
}
