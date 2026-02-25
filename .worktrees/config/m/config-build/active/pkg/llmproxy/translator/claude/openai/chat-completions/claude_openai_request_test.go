package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaude(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "hello"}
		],
		"max_tokens": 1024,
		"temperature": 0.5
	}`)

	got := ConvertOpenAIRequestToClaude("claude-3-5-sonnet", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "claude-3-5-sonnet" {
		t.Errorf("expected model claude-3-5-sonnet, got %s", res.Get("model").String())
	}

	if res.Get("max_tokens").Int() != 1024 {
		t.Errorf("expected max_tokens 1024, got %d", res.Get("max_tokens").Int())
	}

	messages := res.Get("messages").Array()
	if len(messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(messages))
	}
}
