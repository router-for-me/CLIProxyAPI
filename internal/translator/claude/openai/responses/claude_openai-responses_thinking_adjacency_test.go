package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestAssistantContentNeverHasAdjacentThinking(t *testing.T) {
	rawSignature, _ := testClaudeResponsesThinkingSignature(t)
	reasoning := `{"type":"reasoning","encrypted_content":"` + rawSignature + `","summary":[{"type":"summary_text","text":"a"}]}`
	raw := responsesRequestFromItems(reasoning, reasoning, reasoning,
		`{"type":"function_call","call_id":"toolu_1","name":"x","arguments":"{}"}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	kinds := claudeAssistantBlockTypes(t, out)
	for i := 1; i < len(kinds); i++ {
		if kinds[i] == "thinking" && kinds[i-1] == "thinking" {
			t.Fatalf("adjacent thinking blocks are rejected by Anthropic: %v", kinds)
		}
	}
	if len(kinds) != 2 || kinds[0] != "thinking" || kinds[1] != "tool_use" {
		t.Fatalf("blocks = %v, want [thinking tool_use]", kinds)
	}
}

func TestAdjacentThinkingMergeKeepsNonEmptyText(t *testing.T) {
	rawSignature, _ := testClaudeResponsesThinkingSignature(t)
	item := func(text string) string {
		return `{"type":"reasoning","encrypted_content":"` + rawSignature + `","summary":[{"type":"summary_text","text":"` + text + `"}]}`
	}
	raw := responsesRequestFromItems(item(""), item("second"),
		`{"type":"function_call","call_id":"toolu_1","name":"x","arguments":"{}"}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	if got := gjson.GetBytes(out, "messages.0.content.0.thinking").String(); got != "second" {
		t.Fatalf("merged thinking = %q, want %q (empty leading text must not add separators)", got, "second")
	}
}
