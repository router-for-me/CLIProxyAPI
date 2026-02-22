package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCLI(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`)

	got := ConvertClaudeRequestToCLI("gemini-1.5-pro", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gemini-1.5-pro" {
		t.Errorf("expected model gemini-1.5-pro, got %s", res.Get("model").String())
	}

	contents := res.Get("request.contents").Array()
	if len(contents) != 1 {
		t.Errorf("expected 1 content item, got %d", len(contents))
	}
}

func TestConvertClaudeRequestToCLI_SanitizesToolUseThoughtSignature(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"assistant",
				"content":[
					{
						"type":"tool_use",
						"id":"toolu_01",
						"name":"lookup",
						"input":{"q":"hello"}
					}
				]
			}
		]
	}`)

	got := ConvertClaudeRequestToCLI("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)

	part := res.Get("request.contents.0.parts.0")
	if !part.Get("functionCall").Exists() {
		t.Fatalf("expected tool_use to map to functionCall")
	}
	if part.Get("thoughtSignature").String() != geminiCLIClaudeThoughtSignature {
		t.Fatalf("expected thoughtSignature %q, got %q", geminiCLIClaudeThoughtSignature, part.Get("thoughtSignature").String())
	}
}
