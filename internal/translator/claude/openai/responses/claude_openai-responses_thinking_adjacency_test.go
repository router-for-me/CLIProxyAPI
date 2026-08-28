package responses

import (
	"strings"
	"testing"
)

// Anthropic rejects a replayed assistant message that carries two adjacent
// thinking blocks, because it never produces that shape itself. A Responses
// conversation reaches it on an ordinary turn: an agent that thinks twice
// before speaking emits two consecutive reasoning items, and every later turn
// of that conversation is then rejected with
// "`thinking` or `redacted_thinking` blocks in the latest assistant message
// cannot be modified".
func TestConsecutiveReasoningItemsDoNotProduceAdjacentThinkingBlocks(t *testing.T) {
	signature := mustTestSignature(t)
	reasoning := func(text string) string {
		return `{"type":"reasoning","encrypted_content":"` + signature + `","summary":[{"type":"summary_text","text":"` + text + `"}]}`
	}
	raw := responsesRequestFromItems(
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		reasoning("first thought"),
		reasoning("second thought"),
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`,
	)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	got := claudeAssistantBlockTypes(t, out)
	want := []string{"thinking", "text"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("assistant blocks = %v\nwant %v\noutput: %s", got, want, string(out))
	}
	if body := string(out); !strings.Contains(body, "first thought") || !strings.Contains(body, "second thought") {
		t.Fatalf("merged thinking block lost reasoning text: %s", body)
	}
}

// The shape an agent actually produces: think, call a tool, think again, call
// another tool. Tool calls are collected at the end of the assistant message so
// they stay adjacent to their tool_result turn, which leaves the two reasoning
// items side by side and makes every later turn of the conversation fail.
func TestReasoningBetweenToolCallsStaysReplayable(t *testing.T) {
	signature := mustTestSignature(t)
	reasoning := func(text string) string {
		return `{"type":"reasoning","encrypted_content":"` + signature + `","summary":[{"type":"summary_text","text":"` + text + `"}]}`
	}
	call := func(id string) string {
		return `{"type":"function_call","call_id":"` + id + `","name":"shell","arguments":"{\"cmd\":\"ls\"}"}`
	}
	raw := responsesRequestFromItems(
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		reasoning("plan the first call"),
		call("call_one"),
		reasoning("plan the second call"),
		call("call_two"),
		`{"type":"function_call_output","call_id":"call_one","output":"ok"}`,
		`{"type":"function_call_output","call_id":"call_two","output":"ok"}`,
	)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	got := claudeAssistantBlockTypes(t, out)
	want := []string{"thinking", "tool_use", "tool_use"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("assistant blocks = %v\nwant %v\noutput: %s", got, want, string(out))
	}
	if body := string(out); !strings.Contains(body, "plan the first call") || !strings.Contains(body, "plan the second call") {
		t.Fatalf("merged thinking block lost reasoning text: %s", body)
	}
}
