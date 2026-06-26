package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToOpenAI(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [
					{"text": "hello"}
				]
			}
		],
		"generationConfig": {
			"temperature": 0.7,
			"maxOutputTokens": 100,
			"thinkingConfig": {
				"thinkingLevel": "high"
			}
		}
	}`)

	got := ConvertGeminiRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", res.Get("model").String())
	}

	if res.Get("temperature").Float() != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", res.Get("temperature").Float())
	}

	if res.Get("max_tokens").Int() != 100 {
		t.Errorf("expected max_tokens 100, got %d", res.Get("max_tokens").Int())
	}

	if res.Get("reasoning_effort").String() != "high" {
		t.Errorf("expected reasoning_effort high, got %s", res.Get("reasoning_effort").String())
	}

	messages := res.Get("messages").Array()
	if len(messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(messages))
	}

	if messages[0].Get("role").String() != "user" || messages[0].Get("content").String() != "hello" {
		t.Errorf("unexpected user message: %s", messages[0].Raw)
	}
}
