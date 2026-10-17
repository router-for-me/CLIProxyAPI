package thinking_test

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	"github.com/tidwall/gjson"
)

// Codex sends reasoning.effort on the Responses API; the translator emits
// output_config.effort and ApplyThinking then validates it against the
// registered Claude model, so this covers the full request path.
func TestResponsesEffortReachesClaudeAfterApplyThinking(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     string
		wantEffort string
	}{
		{"xhigh passes through on opus-4-8", "claude-opus-4-8", "xhigh", "xhigh"},
		{"xhigh passes through on sonnet-5", "claude-sonnet-5", "xhigh", "xhigh"},
		{"xhigh passes through on opus-5", "claude-opus-5", "xhigh", "xhigh"},
		{"max passes through on opus-4-8", "claude-opus-4-8", "max", "max"},
		{"xhigh clamps to max on opus-4-6", "claude-opus-4-6", "xhigh", "max"},
		{"xhigh clamps to max on sonnet-4-6", "claude-sonnet-4-6", "xhigh", "max"},
		{"high passes through on opus-4-6", "claude-opus-4-6", "high", "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"model":%q,"reasoning":{"effort":%q},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, tt.model, tt.effort)
			translated := responsesclaude.ConvertOpenAIResponsesRequestToClaude(tt.model, []byte(raw), false)
			out, err := thinking.ApplyThinking(translated, tt.model, "openai-response", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v; translated=%s", err, translated)
			}
			root := gjson.ParseBytes(out)
			if got := root.Get("thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive. Output: %s", got, out)
			}
			if got := root.Get("output_config.effort").String(); got != tt.wantEffort {
				t.Fatalf("output_config.effort = %q, want %q. Output: %s", got, tt.wantEffort, out)
			}
		})
	}
}
