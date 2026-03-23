package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// TestStreamingUsage_CacheHit verifies that prompt_tokens includes
// cache_read_input_tokens when translating a Claude message_delta event
// with a cache hit to OpenAI streaming format.
func TestStreamingUsage_CacheHit(t *testing.T) {
	// Simulate a message_delta event from Anthropic with a cache hit:
	// input_tokens=13 (non-cached), cache_read_input_tokens=22000 (cached), output_tokens=5
	messageDelta := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":5,"cache_read_input_tokens":22000,"cache_creation_input_tokens":0}}`)

	var param any
	// Send a message_start first to initialize params
	messageStart := []byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","role":"assistant","content":[],"usage":{"input_tokens":13}}}`)
	ConvertClaudeResponseToOpenAI(context.Background(), "claude-opus", nil, nil, messageStart, &param)

	// Now send the message_delta with usage
	results := ConvertClaudeResponseToOpenAI(context.Background(), "claude-opus", nil, nil, messageDelta, &param)
	if len(results) == 0 {
		t.Fatal("expected at least one result from message_delta")
	}

	result := gjson.ParseBytes(results[0])
	promptTokens := result.Get("usage.prompt_tokens").Int()
	completionTokens := result.Get("usage.completion_tokens").Int()
	totalTokens := result.Get("usage.total_tokens").Int()
	cachedTokens := result.Get("usage.prompt_tokens_details.cached_tokens").Int()

	// prompt_tokens should be input_tokens + cache_creation + cache_read = 13 + 0 + 22000 = 22013
	if promptTokens != 22013 {
		t.Errorf("prompt_tokens: got %d, want 22013", promptTokens)
	}
	if completionTokens != 5 {
		t.Errorf("completion_tokens: got %d, want 5", completionTokens)
	}
	// total_tokens should be prompt_tokens + completion_tokens = 22013 + 5 = 22018
	if totalTokens != 22018 {
		t.Errorf("total_tokens: got %d, want 22018", totalTokens)
	}
	if cachedTokens != 22000 {
		t.Errorf("cached_tokens: got %d, want 22000", cachedTokens)
	}
}

// TestStreamingUsage_CacheCreation verifies that prompt_tokens includes
// cache_creation_input_tokens on a cold-cache request.
func TestStreamingUsage_CacheCreation(t *testing.T) {
	messageDelta := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":22000}}`)

	var param any
	messageStart := []byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","role":"assistant","content":[],"usage":{"input_tokens":13}}}`)
	ConvertClaudeResponseToOpenAI(context.Background(), "claude-opus", nil, nil, messageStart, &param)

	results := ConvertClaudeResponseToOpenAI(context.Background(), "claude-opus", nil, nil, messageDelta, &param)
	if len(results) == 0 {
		t.Fatal("expected at least one result from message_delta")
	}

	result := gjson.ParseBytes(results[0])
	promptTokens := result.Get("usage.prompt_tokens").Int()
	totalTokens := result.Get("usage.total_tokens").Int()

	// prompt_tokens = 13 + 22000 + 0 = 22013
	if promptTokens != 22013 {
		t.Errorf("prompt_tokens: got %d, want 22013", promptTokens)
	}
	// total_tokens = 22013 + 5 = 22018
	if totalTokens != 22018 {
		t.Errorf("total_tokens: got %d, want 22018", totalTokens)
	}
}

// TestStreamingUsage_NoCache verifies correct behavior when there is no caching.
func TestStreamingUsage_NoCache(t *testing.T) {
	messageDelta := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":500,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)

	var param any
	messageStart := []byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","role":"assistant","content":[],"usage":{"input_tokens":500}}}`)
	ConvertClaudeResponseToOpenAI(context.Background(), "claude-opus", nil, nil, messageStart, &param)

	results := ConvertClaudeResponseToOpenAI(context.Background(), "claude-opus", nil, nil, messageDelta, &param)
	if len(results) == 0 {
		t.Fatal("expected at least one result from message_delta")
	}

	result := gjson.ParseBytes(results[0])
	promptTokens := result.Get("usage.prompt_tokens").Int()
	totalTokens := result.Get("usage.total_tokens").Int()

	if promptTokens != 500 {
		t.Errorf("prompt_tokens: got %d, want 500", promptTokens)
	}
	if totalTokens != 550 {
		t.Errorf("total_tokens: got %d, want 550", totalTokens)
	}
}

// TestNonStreamingUsage_CacheHit verifies the non-streaming path correctly
// includes cache_read_input_tokens in prompt_tokens.
func TestNonStreamingUsage_CacheHit(t *testing.T) {
	// Simulate a full non-streaming response with message_start + content + message_delta
	rawJSON := []byte(
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":13}}}\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":13,\"output_tokens\":5,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":0}}\n" +
			"data: {\"type\":\"message_stop\"}\n")

	result := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-opus", nil, nil, rawJSON, nil)
	parsed := gjson.ParseBytes(result)

	promptTokens := parsed.Get("usage.prompt_tokens").Int()
	completionTokens := parsed.Get("usage.completion_tokens").Int()
	totalTokens := parsed.Get("usage.total_tokens").Int()
	cachedTokens := parsed.Get("usage.prompt_tokens_details.cached_tokens").Int()

	if promptTokens != 22013 {
		t.Errorf("prompt_tokens: got %d, want 22013", promptTokens)
	}
	if completionTokens != 5 {
		t.Errorf("completion_tokens: got %d, want 5", completionTokens)
	}
	if totalTokens != 22018 {
		t.Errorf("total_tokens: got %d, want 22018", totalTokens)
	}
	if cachedTokens != 22000 {
		t.Errorf("cached_tokens: got %d, want 22000", cachedTokens)
	}
}
