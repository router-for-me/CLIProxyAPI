package thinking_test

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_ClaudeAdaptiveAutoPreservesUpstreamDefault(t *testing.T) {
	body := []byte(`{
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"auto"}
	}`)

	out, err := thinking.ApplyThinking(body, "claude-opus-4-6", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want %q, body=%s", got, "adaptive", string(out))
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("thinking.budget_tokens should be omitted for adaptive auto, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("output_config.effort should be omitted for adaptive auto, body=%s", string(out))
	}
}

func TestApplyThinking_ClaudeCompatAntigravityRejectsInvalidClaudeEffort(t *testing.T) {
	tests := []struct {
		name   string
		effort string
	}{
		{name: "minimal", effort: "minimal"},
		{name: "xhigh", effort: "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"` + tt.effort + `","includeThoughts":true}}}}`)

			out, err := thinking.ApplyThinking(body, "claude-opus-4-6-thinking", "claude", "antigravity", "antigravity")
			if err == nil {
				t.Fatalf("ApplyThinking() error = nil, body=%s", string(out))
			}

			thinkingErr, ok := err.(*thinking.ThinkingError)
			if !ok {
				t.Fatalf("error type = %T, want *thinking.ThinkingError", err)
			}
			if thinkingErr.Code != thinking.ErrLevelNotSupported {
				t.Fatalf("error code = %q, want %q", thinkingErr.Code, thinking.ErrLevelNotSupported)
			}
			if !strings.Contains(thinkingErr.Message, `valid levels: low, medium, high, max`) {
				t.Fatalf("error message = %q, want valid Claude effort list", thinkingErr.Message)
			}
			if string(out) != string(body) {
				t.Fatalf("returned body should remain unchanged on validation failure\ngot:  %s\nwant: %s", string(out), string(body))
			}
		})
	}
}
