package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_SanitizesToolCallIDsForClaude(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{
				"type": "function_call",
				"call_id": "call.with space:1",
				"name": "Read",
				"arguments": "{\"path\":\"README.md\"}"
			},
			{
				"type": "function_call_output",
				"call_id": "call.with space:1",
				"output": "ok"
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	toolUseID := resultJSON.Get("messages.0.content.0.id").String()
	toolResultID := resultJSON.Get("messages.1.content.0.tool_use_id").String()

	if toolUseID != "call_with_space_1" {
		t.Fatalf("tool_use id = %q, want %q", toolUseID, "call_with_space_1")
	}
	if toolResultID != toolUseID {
		t.Fatalf("tool_result tool_use_id = %q, want same sanitized id %q", toolResultID, toolUseID)
	}
}
