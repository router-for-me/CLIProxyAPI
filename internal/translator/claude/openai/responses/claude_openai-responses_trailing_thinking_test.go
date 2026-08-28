package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

// A replayed conversation can end on a reasoning item, which becomes an
// assistant message whose only block is thinking. Anthropic rejects that with
// "messages.N: The final block in an assistant message cannot be `thinking`."
// The block is unusable as prefill anyway, so the message carries no
// information the request needs and dropping it is lossless.
func TestTrailingThinkingOnlyAssistantMessageIsNotSent(t *testing.T) {
	raw := responsesRequestFromItems(
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"`+mustTestSignature(t)+`"}`,
	)

	out := ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) == 0 {
		t.Fatalf("translator produced no messages. Output: %s", string(out))
	}
	last := messages[len(messages)-1]
	if last.Get("role").String() != "assistant" {
		return
	}
	blocks := last.Get("content").Array()
	if len(blocks) == 0 {
		t.Fatalf("assistant message has no content blocks. Output: %s", string(out))
	}
	if kind := blocks[len(blocks)-1].Get("type").String(); kind == "thinking" || kind == "redacted_thinking" {
		t.Fatalf("final assistant block is %q, which Anthropic rejects. Output: %s", kind, string(out))
	}
}
