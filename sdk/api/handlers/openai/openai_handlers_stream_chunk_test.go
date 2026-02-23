package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertChatCompletionsStreamChunkToCompletions_DropsUsageOnTerminalFinishChunk(t *testing.T) {
	chunk := []byte(`{
		"id":"chatcmpl-1",
		"object":"chat.completion.chunk",
		"created":1,
		"model":"gpt-x",
		"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
	}`)

	converted := convertChatCompletionsStreamChunkToCompletions(chunk)
	if converted == nil {
		t.Fatalf("expected converted chunk, got nil")
	}

	if gjson.GetBytes(converted, "usage").Exists() {
		t.Fatalf("expected usage to be omitted on terminal finish chunk, got %s", gjson.GetBytes(converted, "usage").Raw)
	}
	if got := gjson.GetBytes(converted, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason=%q, want stop", got)
	}
}

func TestConvertChatCompletionsStreamChunkToCompletions_PreservesUsageOnlyChunk(t *testing.T) {
	chunk := []byte(`{
		"id":"chatcmpl-2",
		"object":"chat.completion.chunk",
		"created":2,
		"model":"gpt-x",
		"choices":[],
		"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
	}`)

	converted := convertChatCompletionsStreamChunkToCompletions(chunk)
	if converted == nil {
		t.Fatalf("expected converted chunk, got nil")
	}

	if !gjson.GetBytes(converted, "usage").Exists() {
		t.Fatalf("expected usage to be present for usage-only chunk")
	}
	if got := gjson.GetBytes(converted, "choices.#").Int(); got != 0 {
		t.Fatalf("choices count=%d, want 0", got)
	}
}
