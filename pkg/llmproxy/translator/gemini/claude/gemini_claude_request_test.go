package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToGemini(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`)

	got := ConvertClaudeRequestToGemini("gemini-1.5-pro", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gemini-1.5-pro" {
		t.Errorf("expected model gemini-1.5-pro, got %s", res.Get("model").String())
	}

	contents := res.Get("contents").Array()
	if len(contents) != 1 {
		t.Errorf("expected 1 content item, got %d", len(contents))
	}
}

func TestConvertClaudeRequestToGeminiRemovesUnsupportedSchemaFields(t *testing.T) {
	input := []byte(`{
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{
				"name":"lookup",
				"description":"lookup values",
				"input_schema":{
					"type":"object",
					"$id":"urn:tool:lookup",
					"properties":{"q":{"type":"string"}},
					"patternProperties":{"^x-":{"type":"string"}}
				}
			}
		]
	}`)

	got := ConvertClaudeRequestToGemini("gemini-1.5-pro", input, false)
	res := gjson.ParseBytes(got)

	schema := res.Get("tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("expected parametersJsonSchema to exist")
	}
	if schema.Get("$id").Exists() {
		t.Fatalf("expected $id to be removed from parametersJsonSchema")
	}
	if schema.Get("patternProperties").Exists() {
		t.Fatalf("expected patternProperties to be removed from parametersJsonSchema")
	}
}

func TestConvertClaudeRequestToGeminiSkipsMetadataOnlyMessageBlocks(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"metadata","note":"ignore"}]},
			{"role":"user","content":[{"type":"text","text":"hello"}]}
		]
	}`)

	got := ConvertClaudeRequestToGemini("gemini-1.5-pro", input, false)
	res := gjson.ParseBytes(got)

	contents := res.Get("contents").Array()
	if len(contents) != 1 {
		t.Fatalf("expected only 1 valid content entry, got %d", len(contents))
	}
	if contents[0].Get("parts.0.text").String() != "hello" {
		t.Fatalf("expected text content to be preserved")
	}
}
