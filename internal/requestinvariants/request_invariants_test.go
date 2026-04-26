package requestinvariants

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIChatToolCallReasoning_PatchesWhenThinkingEnabled(t *testing.T) {
	body := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
		]
	}`)

	out, patched, err := NormalizeOpenAIChatToolCallReasoning(body, true)
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatToolCallReasoning() error = %v", err)
	}
	if patched != 1 {
		t.Fatalf("patched = %d, want 1", patched)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != AssistantReasoningPlaceholder {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, AssistantReasoningPlaceholder)
	}
}

func TestNormalizeClaudeMessagesToolUseReasoningPrefix_PrependsLatestReasoning(t *testing.T) {
	body := []byte(`{
		"thinking":{"type":"adaptive"},
		"messages":[
			{"role":"assistant","content":[{"type":"thinking","thinking":"previous reasoning"},{"type":"text","text":"answer"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}
		]
	}`)

	out, patched, err := NormalizeClaudeMessagesToolUseReasoningPrefix(body, true)
	if err != nil {
		t.Fatalf("NormalizeClaudeMessagesToolUseReasoningPrefix() error = %v", err)
	}
	if patched != 1 {
		t.Fatalf("patched = %d, want 1", patched)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.text").String(); got != "previous reasoning" {
		t.Fatalf("messages.1.content.0.text = %q, want %q", got, "previous reasoning")
	}
	if got := gjson.GetBytes(out, "messages.1.content.1.type").String(); got != "tool_use" {
		t.Fatalf("messages.1.content.1.type = %q, want %q", got, "tool_use")
	}
}

func TestNormalizeClaudeMessagesToolUseReasoningPrefix_SkipsExistingPrefix(t *testing.T) {
	body := []byte(`{
		"thinking":{"type":"adaptive"},
		"messages":[
			{"role":"assistant","content":[{"type":"text","text":"existing prefix"},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]}
		]
	}`)

	out, patched, err := NormalizeClaudeMessagesToolUseReasoningPrefix(body, true)
	if err != nil {
		t.Fatalf("NormalizeClaudeMessagesToolUseReasoningPrefix() error = %v", err)
	}
	if patched != 0 {
		t.Fatalf("patched = %d, want 0", patched)
	}
	if got := gjson.GetBytes(out, "messages.0.content.#").Int(); got != 2 {
		t.Fatalf("content length = %d, want 2", got)
	}
}
