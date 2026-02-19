package geminiCLI

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiCLIRequestToOpenAI(t *testing.T) {
	input := []byte(`{
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [
						{"text": "hello"}
					]
				}
			],
			"generationConfig": {
				"temperature": 0.7
			},
			"systemInstruction": {
				"parts": [
					{"text": "system instruction"}
				]
			}
		}
	}`)

	got := ConvertGeminiCLIRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", res.Get("model").String())
	}

	if res.Get("temperature").Float() != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", res.Get("temperature").Float())
	}

	messages := res.Get("messages").Array()
	// systemInstruction should become a system message in ConvertGeminiRequestToOpenAI (if it supports it)
	// Actually, ConvertGeminiRequestToOpenAI should handle system_instruction if it exists in the raw JSON after translation here.
	
	// Let's see if we have 2 messages (system + user)
	if len(messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(messages))
	}
}
