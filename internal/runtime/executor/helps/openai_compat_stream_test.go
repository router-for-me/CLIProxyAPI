package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAICompatStreamPayloadMovesThinkingTagsAcrossChunks(t *testing.T) {
	state := NewOpenAICompatStreamState()
	chunks := []string{
		`{"id":"x","choices":[{"index":0,"delta":{"content":"<"}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"content":"thinking>plan "}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"content":"step</thinking>answer"}}]}`,
	}

	var visible, reasoning string
	for _, raw := range chunks {
		out := NormalizeOpenAICompatStreamPayload([]byte(raw), state)
		visible += gjson.GetBytes(out, "choices.0.delta.content").String()
		reasoning += gjson.GetBytes(out, "choices.0.delta.reasoning_content").String()
	}
	if visible != "answer" {
		t.Fatalf("visible content = %q, want %q", visible, "answer")
	}
	if reasoning != "plan step" {
		t.Fatalf("reasoning content = %q, want %q", reasoning, "plan step")
	}
}

func TestNormalizeOpenAICompatNonStreamPayloadMovesThinkingTags(t *testing.T) {
	raw := []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"<thinking>plan</thinking>answer"},"finish_reason":"stop"}]}`)
	out := NormalizeOpenAICompatNonStreamPayload(raw, NewOpenAICompatStreamState())
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "answer" {
		t.Fatalf("visible content = %q, want %q", got, "answer")
	}
	if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "plan" {
		t.Fatalf("reasoning_content = %q, want %q", got, "plan")
	}
}

func TestSyntheticOpenAICompatReasoningFinishChunk(t *testing.T) {
	state := NewOpenAICompatStreamState()
	NormalizeOpenAICompatStreamPayload([]byte(`{"id":"x","model":"m","created":7,"choices":[{"index":0,"delta":{"reasoning_content":"plan"},"finish_reason":null}]}`), state)

	finish := SyntheticOpenAICompatReasoningFinishChunk(state)
	if len(finish) == 0 {
		t.Fatal("expected a synthetic terminal chunk")
	}
	if got := gjson.GetBytes(finish, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop", got)
	}
	if got := gjson.GetBytes(finish, "model").String(); got != "m" {
		t.Fatalf("model = %q, want m", got)
	}
	if got := SyntheticOpenAICompatReasoningFinishChunk(state); len(got) != 0 {
		t.Fatalf("second synthetic terminal chunk = %s, want none", got)
	}
}
