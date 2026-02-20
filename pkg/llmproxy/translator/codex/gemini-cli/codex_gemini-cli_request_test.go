package geminiCLI

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiCLIRequestToCodex(t *testing.T) {
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
			"systemInstruction": {
				"parts": [
					{"text": "system instruction"}
				]
			}
		}
	}`)

	got := ConvertGeminiCLIRequestToCodex("gpt-4o", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", res.Get("model").String())
	}

	inputArray := res.Get("input").Array()
	if len(inputArray) < 1 {
		t.Errorf("expected at least 1 input item, got %d", len(inputArray))
	}
}
