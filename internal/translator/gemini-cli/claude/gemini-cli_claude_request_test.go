package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCLI_ToolChoice_SpecificTool(t *testing.T) {
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

	output := ConvertClaudeRequestToCLI("gemini-3-flash-preview", inputJSON, false)

	if got := gjson.GetBytes(output, "request.toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("Expected request.toolConfig.functionCallingConfig.mode 'ANY', got '%s'", got)
	}
	allowed := gjson.GetBytes(output, "request.toolConfig.functionCallingConfig.allowedFunctionNames").Array()
	if len(allowed) != 1 || allowed[0].String() != "json" {
		t.Fatalf("Expected allowedFunctionNames ['json'], got %s", gjson.GetBytes(output, "request.toolConfig.functionCallingConfig.allowedFunctionNames").Raw)
	}
}

func TestConvertClaudeRequestToCLI_FlattensStructuredToolResultContent(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "json-call-1", "name": "json", "input": {"ok": true}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "json-call-1",
						"content": [
							{"type": "text", "text": "alpha"},
							{"type": "image", "source": {"type": "base64", "media_type": "image/png"}}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToCLI("gemini-3-flash-preview", inputJSON, false)

	got := gjson.GetBytes(output, "request.contents.1.parts.0.functionResponse.response.result").String()
	want := "alpha\n\n{\"type\":\"image\",\"source\":{\"type\":\"base64\",\"media_type\":\"image/png\"}}"
	if got != want {
		t.Fatalf("functionResponse.response.result = %q, want %q", got, want)
	}
}
