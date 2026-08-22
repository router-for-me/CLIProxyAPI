package executor

import (
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestTranslateCodexRequestPairDoesNotForwardClaudeMaxTokens(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`)
	_, body, err := translateCodexRequestPair(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5.6-sol", payload, payload, true)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}
	if gjson.GetBytes(body, "max_output_tokens").Exists() {
		t.Fatalf("max_output_tokens must not reach the Codex subscription upstream; body=%s", body)
	}
}

func TestTranslateCodexRequestPairAcceptsContemporaryClaudeCodeRequest(t *testing.T) {
	payload := []byte(`{
		"model":"claude/gpt-5.6-sol",
		"max_tokens":32000,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"run the check"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_current_1","name":"Bash","input":{"command":"go test ./..."}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_current_1","content":[{"type":"text","text":"ok"}]}]},
			{"role":"system","content":[{"type":"text","text":"The Stop hook requires a final response."}]},
			{"role":"user","content":[{"type":"text","text":"finish"}]}
		],
		"metadata":{"user_id":"user_test_account__session_test"},
		"stream":true,
		"system":[
			{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"Follow repository rules.","cache_control":{"type":"ephemeral"}}
		],
		"thinking":{"budget_tokens":31999,"type":"enabled"},
		"tools":[
			{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
			{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}}
		]
	}`)
	_, body, err := translateCodexRequestPair(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5.6-sol", payload, payload, true)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}

	parsed := gjson.ParseBytes(body)
	if got := parsed.Get("reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, body)
	}
	if got := parsed.Get("input.0.content.0.text").String(); got != "You are Claude Code." {
		t.Fatalf("first system text = %q; body=%s", got, body)
	}
	if got := parsed.Get("input.0.content.1.text").String(); got != "Follow repository rules." {
		t.Fatalf("second system text = %q; body=%s", got, body)
	}
	inputs := parsed.Get("input").Array()
	if len(inputs) != 6 {
		t.Fatalf("input items = %d, want 6; body=%s", len(inputs), body)
	}
	if got := inputs[2].Get("type").String(); got != "function_call" {
		t.Fatalf("tool call type = %q, want function_call; body=%s", got, body)
	}
	if got := inputs[3].Get("type").String(); got != "function_call_output" {
		t.Fatalf("tool result type = %q, want function_call_output; body=%s", got, body)
	}
	if got := inputs[4].Get("role").String(); got != "user" {
		t.Fatalf("hook role = %q, want user; body=%s", got, body)
	}
	if got := inputs[4].Get("content.0.text").String(); !strings.Contains(got, "The Stop hook requires a final response.") {
		t.Fatalf("hook text = %q; body=%s", got, body)
	}
	if got := parsed.Get("tools.#").Int(); got != 2 {
		t.Fatalf("tools = %d, want 2; body=%s", got, body)
	}
	for _, field := range []string{"max_output_tokens", "thinking", "metadata"} {
		if parsed.Get(field).Exists() {
			t.Fatalf("Claude-only field %q reached Codex upstream; body=%s", field, body)
		}
	}
}
