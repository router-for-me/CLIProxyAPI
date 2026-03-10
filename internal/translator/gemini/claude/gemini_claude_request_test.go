package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToGemini_ToolChoice_SpecificTool(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "hi"}
				]
			}
		],
		"tools": [
			{
				"name": "json",
				"description": "A JSON tool",
				"input_schema": {
					"type": "object",
					"properties": {}
				}
			}
		],
		"tool_choice": {"type": "tool", "name": "json"}
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	if got := gjson.GetBytes(output, "toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("Expected toolConfig.functionCallingConfig.mode 'ANY', got '%s'", got)
	}
	allowed := gjson.GetBytes(output, "toolConfig.functionCallingConfig.allowedFunctionNames").Array()
	if len(allowed) != 1 || allowed[0].String() != "json" {
		t.Fatalf("Expected allowedFunctionNames ['json'], got %s", gjson.GetBytes(output, "toolConfig.functionCallingConfig.allowedFunctionNames").Raw)
	}
}

func TestConvertClaudeRequestToGemini_ImageContent_Base64(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "describe image"},
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "iVBORw0KGgoAAAANSUhEUg=="
						}
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3.1-pro-preview", inputJSON, false)

	if got := gjson.GetBytes(output, "contents.0.role").String(); got != "user" {
		t.Fatalf("Expected role %q, got %q", "user", got)
	}
	if got := gjson.GetBytes(output, "contents.0.parts.0.text").String(); got != "describe image" {
		t.Fatalf("Expected text part %q, got %q", "describe image", got)
	}
	if got := gjson.GetBytes(output, "contents.0.parts.1.inline_data.mime_type").String(); got != "image/png" {
		t.Fatalf("Expected mime type %q, got %q", "image/png", got)
	}
	if got := gjson.GetBytes(output, "contents.0.parts.1.inline_data.data").String(); got != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("Unexpected inline_data.data: %q", got)
	}
}
