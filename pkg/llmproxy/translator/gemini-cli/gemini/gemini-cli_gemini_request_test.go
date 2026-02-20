package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToGeminiCLI(t *testing.T) {
	input := []byte(`{
		"model": "gemini-1.5-pro",
		"contents": [
			{
				"parts": [
					{"text": "hello"}
				]
			}
		]
	}`)

	got := ConvertGeminiRequestToGeminiCLI("gemini-1.5-pro", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gemini-1.5-pro" {
		t.Errorf("expected model gemini-1.5-pro, got %s", res.Get("model").String())
	}

	contents := res.Get("request.contents").Array()
	if len(contents) != 1 {
		t.Errorf("expected 1 content, got %d", len(contents))
	}

	if contents[0].Get("role").String() != "user" {
		t.Errorf("expected role user, got %s", contents[0].Get("role").String())
	}
}
