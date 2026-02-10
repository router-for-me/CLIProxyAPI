package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_ContentStringAndArrayFormats(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "gpt-5",
			"input": [
				{"role": "system", "content": "You are helpful"},
				{"role": "user", "content": "Hello"}
			]
		}`)

		output := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-flash", inputJSON, false)
		outputStr := string(output)

		if got := gjson.Get(outputStr, "system_instruction.parts.0.text").String(); got != "You are helpful" {
			t.Fatalf("unexpected system instruction text: got %q", got)
		}

		contents := gjson.Get(outputStr, "contents")
		if !contents.Exists() || !contents.IsArray() {
			t.Fatalf("contents should be an array")
		}
		if len(contents.Array()) != 1 {
			t.Fatalf("expected 1 content item, got %d", len(contents.Array()))
		}
		if got := gjson.Get(outputStr, "contents.0.role").String(); got != "user" {
			t.Fatalf("unexpected role for string message: got %q", got)
		}
		if got := gjson.Get(outputStr, "contents.0.parts.0.text").String(); got != "Hello" {
			t.Fatalf("unexpected text for string message: got %q", got)
		}
	})

	t.Run("array content", func(t *testing.T) {
		inputJSON := []byte(`{
			"model": "gpt-5",
			"input": [
				{
					"role": "system",
					"content": [
						{"type": "input_text", "text": "You are helpful"},
						{"type": "input_text", "text": "Be concise"}
					]
				},
				{
					"role": "user",
					"content": [
						{"type": "input_text", "text": "Hello"}
					]
				}
			]
		}`)

		output := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-flash", inputJSON, false)
		outputStr := string(output)

		if got := gjson.Get(outputStr, "system_instruction.parts.0.text").String(); got != "You are helpful\nBe concise" {
			t.Fatalf("unexpected array system instruction text: got %q", got)
		}
		if got := gjson.Get(outputStr, "contents.0.role").String(); got != "user" {
			t.Fatalf("unexpected role for array message: got %q", got)
		}
		if got := gjson.Get(outputStr, "contents.0.parts.0.text").String(); got != "Hello" {
			t.Fatalf("unexpected text for array message: got %q", got)
		}
	})
}
