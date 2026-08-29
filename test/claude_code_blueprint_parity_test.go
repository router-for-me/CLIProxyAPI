package test

import (
	"strings"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeCodeSentinel_HookReminderSurvivesCodexTranslation(t *testing.T) {
	payload := []byte(`{
		"model":"claude-opus-5",
		"max_tokens":1024,
		"system":"Follow repository policy.",
		"messages":[
			{"role":"user","content":"run tests"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_stop_1","name":"Bash","input":{"command":"go test ./..."}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_stop_1","content":"ok"}]},
			{"role":"system","content":[{"type":"text","text":"The Stop hook requires a final protocol-safe response."}]},
			{"role":"user","content":"finish"}
		]
	}`)

	translated := sdktranslator.TranslateRequest(
		sdktranslator.FormatClaude,
		sdktranslator.FormatCodex,
		"gpt-5.6-sol",
		payload,
		true,
	)
	inputs := gjson.GetBytes(translated, "input").Array()
	if len(inputs) != 6 {
		t.Fatalf("translated input items = %d, want 6; output=%s", len(inputs), translated)
	}
	if got := inputs[0].Get("content.0.text").String(); got != "Follow repository policy." {
		t.Fatalf("system policy = %q", got)
	}
	if got := inputs[2].Get("call_id").String(); got != "toolu_stop_1" {
		t.Fatalf("tool call id = %q, want toolu_stop_1", got)
	}
	if got := inputs[3].Get("call_id").String(); got != "toolu_stop_1" {
		t.Fatalf("tool result id = %q, want toolu_stop_1", got)
	}
	if got := inputs[4].Get("content.0.text").String(); !strings.Contains(got, "The Stop hook requires a final protocol-safe response.") {
		t.Fatalf("hook reminder = %q", got)
	}
	if got := inputs[5].Get("content.0.text").String(); got != "finish" {
		t.Fatalf("post-hook user turn = %q, want finish", got)
	}
	if gjson.GetBytes(translated, "max_output_tokens").Exists() {
		t.Fatalf("max_output_tokens must not reach the Codex subscription upstream; output=%s", translated)
	}
}
