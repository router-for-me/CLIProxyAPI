package claude

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAINonStreamingToAnthropicPreservesUsageWhenChoicesEmpty(t *testing.T) {
	chunks := convertOpenAINonStreamingToAnthropic([]byte(`{
		"id":"resp-1",
		"model":"gpt-test",
		"choices":[],
		"usage":{"prompt_tokens":5,"completion_tokens":7}
	}`))

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	result := gjson.ParseBytes(chunks[0])
	if got := result.Get("usage.input_tokens").Int(); got != 5 {
		t.Fatalf("input_tokens = %d, want 5", got)
	}
	if got := result.Get("usage.output_tokens").Int(); got != 7 {
		t.Fatalf("output_tokens = %d, want 7", got)
	}
}

func TestConvertOpenAIResponseToClaudeNonStreamDefaultsStopReasonWhenChoicesEmpty(t *testing.T) {
	result := gjson.ParseBytes(ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"gpt-test",
		nil,
		nil,
		[]byte(`{
			"id":"resp-2",
			"model":"gpt-test",
			"choices":[],
			"usage":{"prompt_tokens":11,"completion_tokens":13}
		}`),
		nil,
	))

	if got := result.Get("usage.input_tokens").Int(); got != 11 {
		t.Fatalf("input_tokens = %d, want 11", got)
	}
	if got := result.Get("usage.output_tokens").Int(); got != 13 {
		t.Fatalf("output_tokens = %d, want 13", got)
	}
	if got := result.Get("stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want %q", got, "end_turn")
	}
}
