package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesToolChoiceObject(t *testing.T) {
	t.Parallel()

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-test", []byte(`{
		"input": "hello",
		"tool_choice": {"type":"function","function":{"name":"lookup"}}
	}`), false)

	toolChoice := gjson.ParseBytes(out).Get("tool_choice")
	if !toolChoice.IsObject() {
		t.Fatalf("tool_choice = %s, want object", toolChoice.Raw)
	}
	if got := toolChoice.Get("function.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want lookup; output=%s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesImageDetail(t *testing.T) {
	t.Parallel()

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-test", []byte(`{
		"input": [{
			"role": "user",
			"content": [{
				"type": "input_image",
				"image_url": "data:image/png;base64,abc",
				"detail": "high"
			}]
		}]
	}`), false)

	detail := gjson.ParseBytes(out).Get("messages.0.content.0.image_url.detail").String()
	if detail != "high" {
		t.Fatalf("image detail = %q, want high; output=%s", detail, string(out))
	}
}
