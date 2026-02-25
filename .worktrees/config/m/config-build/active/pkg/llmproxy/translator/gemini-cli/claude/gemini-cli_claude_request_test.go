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

func TestConvertClaudeRequestToCLI_StripsThoughtSignatureFromToolArgs(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"assistant",
				"content":[
					{
						"type":"tool_use",
						"id":"toolu_01",
						"name":"lookup",
						"input":{"q":"hello","thought_signature":"not-base64"}
					}
				]
			}
		]
	}`)

	got := ConvertClaudeRequestToCLI("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)

	args := res.Get("request.contents.0.parts.0.functionCall.args")
	if !args.Exists() {
		t.Fatalf("expected functionCall args to exist")
	}
	if args.Get("q").String() != "hello" {
		t.Fatalf("expected q arg to be preserved, got %q", args.Get("q").String())
	}
	if args.Get("thought_signature").Exists() {
		t.Fatalf("expected thought_signature to be stripped from tool args")
	}
}
