package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaudePreservesReasoningContent(t *testing.T) {
	input := []byte(`{
		"model":"claude-sonnet",
		"messages":[
			{"role":"assistant","content":"","reasoning_content":"r1","tool_calls":[{"id":"call_1","type":"function","function":{"name":"do_work","arguments":"{}"}}]}
		]
	}`)

	out := ConvertOpenAIRequestToClaude("claude-sonnet", input, false)

	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "thinking" {
		t.Fatalf("messages.0.content.0.type = %q, want %q", got, "thinking")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "r1" {
		t.Fatalf("messages.0.content.0.thinking = %q, want %q", got, "r1")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.type").String(); got != "tool_use" {
		t.Fatalf("messages.0.content.1.type = %q, want %q", got, "tool_use")
	}
}
