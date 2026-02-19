package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "hello"}
				]
			}
		],
		"max_output_tokens": 100
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "claude-3-5-sonnet" {
		t.Errorf("expected model claude-3-5-sonnet, got %s", res.Get("model").String())
	}

	if res.Get("max_tokens").Int() != 100 {
		t.Errorf("expected max_tokens 100, got %d", res.Get("max_tokens").Int())
	}

	messages := res.Get("messages").Array()
	if len(messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(messages))
	}
}
