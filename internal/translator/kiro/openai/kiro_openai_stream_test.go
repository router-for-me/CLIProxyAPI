package openai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestBuildOpenAISSEUsageIncludesCachedPromptDetails(t *testing.T) {
	state := NewOpenAIStreamState("claude-haiku-4-5")
	chunk := BuildOpenAISSEUsage(state, usage.Detail{
		InputTokens:         290,
		OutputTokens:        1,
		CacheReadTokens:     3822,
		CacheCreationTokens: 17,
	})

	payload := parseOpenAIJSON(t, []byte(chunk))
	usagePayload := openAIUsagePayload(t, payload)
	assertOpenAINumber(t, usagePayload, "prompt_tokens", 4129)
	assertOpenAINumber(t, usagePayload, "completion_tokens", 1)
	assertOpenAINumber(t, usagePayload, "total_tokens", 4130)

	promptDetails, ok := usagePayload["prompt_tokens_details"].(map[string]interface{})
	if !ok {
		t.Fatalf("prompt_tokens_details missing or wrong type: %#v", usagePayload["prompt_tokens_details"])
	}
	assertOpenAINumber(t, promptDetails, "cached_tokens", 3822)
}

func TestConvertKiroStreamToOpenAIPreservesCacheUsage(t *testing.T) {
	var param any
	raw := []byte(`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":290,"output_tokens":1,"cache_read_input_tokens":3822,"cache_creation_input_tokens":17}}`)

	chunks := ConvertKiroStreamToOpenAI(context.Background(), "claude-haiku-4-5", nil, nil, raw, &param)
	var usagePayload map[string]interface{}
	for _, chunk := range chunks {
		payload := parseOpenAIJSON(t, chunk)
		if usageValue, ok := payload["usage"].(map[string]interface{}); ok {
			usagePayload = usageValue
			break
		}
	}
	if usagePayload == nil {
		rawChunks, _ := json.Marshal(chunks)
		t.Fatalf("usage chunk missing from chunks: %s", rawChunks)
	}
	assertOpenAINumber(t, usagePayload, "prompt_tokens", 4129)
	assertOpenAINumber(t, usagePayload, "completion_tokens", 1)
	assertOpenAINumber(t, usagePayload, "total_tokens", 4130)

	promptDetails, ok := usagePayload["prompt_tokens_details"].(map[string]interface{})
	if !ok {
		t.Fatalf("prompt_tokens_details missing or wrong type: %#v", usagePayload["prompt_tokens_details"])
	}
	assertOpenAINumber(t, promptDetails, "cached_tokens", 3822)
}
