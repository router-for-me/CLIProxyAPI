package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToGemini(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"parts": [
					{"text": "hello"}
				]
			},
			{
				"parts": [
					{"text": "hi"}
				]
			}
		]
	}`)

	got := ConvertGeminiRequestToGemini("model", input, false)
	res := gjson.ParseBytes(got)

	contents := res.Get("contents").Array()
	if len(contents) != 2 {
		t.Errorf("expected 2 contents, got %d", len(contents))
	}

	if contents[0].Get("role").String() != "user" {
		t.Errorf("expected first role user, got %s", contents[0].Get("role").String())
	}

	if contents[1].Get("role").String() != "model" {
		t.Errorf("expected second role model, got %s", contents[1].Get("role").String())
	}
}

func TestConvertGeminiRequestToGemini_SanitizesThoughtSignatureOnModelParts(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"thoughtSignature": "\\claude#abc"},
					{"functionCall": {"name": "tool", "args": {}}}
				]
			}
		]
	}`)

	got := ConvertGeminiRequestToGemini("model", input, false)
	res := gjson.ParseBytes(got)

	for i, part := range res.Get("contents.0.parts").Array() {
		if part.Get("thoughtSignature").String() != "skip_thought_signature_validator" {
			t.Fatalf("part[%d] thoughtSignature not sanitized: %s", i, part.Get("thoughtSignature").String())
		}
	}
}
