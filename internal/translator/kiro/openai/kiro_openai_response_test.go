package openai

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestBuildOpenAIResponseIncludesCachedPromptDetails(t *testing.T) {
	response := BuildOpenAIResponseWithReasoning("ok", "", nil, "claude-haiku-4-5", usage.Detail{
		InputTokens:         290,
		OutputTokens:        1,
		CacheReadTokens:     3822,
		CacheCreationTokens: 17,
	}, "end_turn")

	payload := parseOpenAIJSON(t, response)
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

func TestConvertKiroNonStreamToOpenAIPreservesCacheUsage(t *testing.T) {
	claudeResponse := []byte(`{
		"type":"message",
		"role":"assistant",
		"model":"claude-haiku-4-5",
		"content":[{"type":"text","text":"ok"}],
		"stop_reason":"end_turn",
		"usage":{
			"input_tokens":290,
			"output_tokens":1,
			"cache_read_input_tokens":3822,
			"cache_creation_input_tokens":17
		}
	}`)

	response := ConvertKiroNonStreamToOpenAI(nil, "claude-haiku-4-5", nil, nil, claudeResponse, nil)
	payload := parseOpenAIJSON(t, response)
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

func parseOpenAIJSON(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal OpenAI payload: %v", err)
	}
	return payload
}

func openAIUsagePayload(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	usagePayload, ok := payload["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage payload missing or wrong type: %#v", payload["usage"])
	}
	return usagePayload
}

func assertOpenAINumber(t *testing.T, payload map[string]interface{}, key string, want int64) {
	t.Helper()
	got, ok := payload[key].(float64)
	if !ok {
		t.Fatalf("%s missing or wrong type: %#v", key, payload[key])
	}
	if int64(got) != want {
		t.Fatalf("%s = %d, want %d", key, int64(got), want)
	}
}
