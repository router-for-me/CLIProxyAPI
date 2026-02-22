package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodex(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`)

	got := ConvertClaudeRequestToCodex("gpt-4o", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", res.Get("model").String())
	}

	inputArray := res.Get("input").Array()
	if len(inputArray) < 1 {
		t.Errorf("expected at least 1 input item, got %d", len(inputArray))
	}
}

func TestConvertClaudeRequestToCodex_CustomToolConvertedToFunctionSchema(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{"role": "user", "content": "hello"}
		],
		"tools": [
			{
				"type": "custom",
				"name": "apply_patch",
				"description": "Apply patch with grammar constraints",
				"format": {
					"type": "grammar",
					"grammar": "start: /[\\s\\S]*/"
				}
			}
		]
	}`)

	got := ConvertClaudeRequestToCodex("gpt-4o", input, true)
	res := gjson.ParseBytes(got)

	if toolType := res.Get("tools.0.type").String(); toolType != "function" {
		t.Fatalf("expected tools[0].type function, got %s", toolType)
	}
	if toolName := res.Get("tools.0.name").String(); toolName != "apply_patch" {
		t.Fatalf("expected tools[0].name apply_patch, got %s", toolName)
	}
	if paramType := res.Get("tools.0.parameters.type").String(); paramType != "object" {
		t.Fatalf("expected tools[0].parameters.type object, got %s", paramType)
	}
}
