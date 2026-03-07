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

func TestConvertClaudeRequestToCLI_ToolResultUsesOriginalToolNameFromMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "fs_readFile-1", "name": "fs.readFile", "input": {"path": "a.txt"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "fs_readFile-1", "content": "ok"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToCLI("gemini-3-flash-preview", inputJSON, false)

	if got := gjson.GetBytes(output, "request.contents.0.parts.0.functionCall.name").String(); got != "fs.readFile" {
		t.Fatalf("Expected tool_use function name %q, got %q", "fs.readFile", got)
	}
	if got := gjson.GetBytes(output, "request.contents.1.parts.0.functionResponse.name").String(); got != "fs.readFile" {
		t.Fatalf("Expected tool_result function name %q, got %q", "fs.readFile", got)
	}
}
