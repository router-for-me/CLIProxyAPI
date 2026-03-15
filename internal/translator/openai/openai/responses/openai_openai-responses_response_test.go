package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// minimal chat completion response for non-stream tests
const minimalChatCompletion = `{
	"id":"chatcmpl-test",
	"object":"chat.completion",
	"created":1700000000,
	"model":"gpt-5",
	"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
}`

// When the translated request carries max_output_tokens (Responses-native field),
// the response must echo it back.
func TestNonStream_MaxOutputTokens_Direct(t *testing.T) {
	req := []byte(`{"model":"gpt-5","max_output_tokens":4096}`)
	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "gpt-5", req, req, []byte(minimalChatCompletion), nil,
	)
	got := gjson.Get(resp, "max_output_tokens").Int()
	if got != 4096 {
		t.Errorf("max_output_tokens = %d, want 4096", got)
	}
}

// After promoteMaxTokens rewrites max_tokens → max_completion_tokens,
// the response converter must still reconstruct max_output_tokens from the
// promoted field.
func TestNonStream_MaxCompletionTokens_Fallback(t *testing.T) {
	// This is what the translated request looks like after promoteMaxTokens()
	req := []byte(`{"model":"gpt-5","max_completion_tokens":2048}`)
	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "gpt-5", req, req, []byte(minimalChatCompletion), nil,
	)
	got := gjson.Get(resp, "max_output_tokens").Int()
	if got != 2048 {
		t.Errorf("max_output_tokens = %d, want 2048 (from max_completion_tokens fallback)", got)
	}
}

// Legacy max_tokens (chat completion style) must still be recognized.
func TestNonStream_MaxTokens_LegacyFallback(t *testing.T) {
	req := []byte(`{"model":"gpt-5","max_tokens":1024}`)
	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "gpt-5", req, req, []byte(minimalChatCompletion), nil,
	)
	got := gjson.Get(resp, "max_output_tokens").Int()
	if got != 1024 {
		t.Errorf("max_output_tokens = %d, want 1024 (from max_tokens legacy fallback)", got)
	}
}

// max_output_tokens takes priority over max_completion_tokens and max_tokens.
func TestNonStream_MaxOutputTokens_Priority(t *testing.T) {
	req := []byte(`{"model":"gpt-5","max_output_tokens":8192,"max_completion_tokens":4096,"max_tokens":2048}`)
	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "gpt-5", req, req, []byte(minimalChatCompletion), nil,
	)
	got := gjson.Get(resp, "max_output_tokens").Int()
	if got != 8192 {
		t.Errorf("max_output_tokens = %d, want 8192 (max_output_tokens has priority)", got)
	}
}

// No token limit fields → max_output_tokens should be absent.
func TestNonStream_NoTokenLimit(t *testing.T) {
	req := []byte(`{"model":"gpt-5"}`)
	resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
		context.Background(), "gpt-5", req, req, []byte(minimalChatCompletion), nil,
	)
	if gjson.Get(resp, "max_output_tokens").Exists() {
		t.Error("max_output_tokens should be absent when no token limit is set")
	}
}
