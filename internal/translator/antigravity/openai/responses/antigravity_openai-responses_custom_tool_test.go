package responses

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Regression test for #7422: an Antigravity /v1/responses request whose input
// contains custom_tool_call / custom_tool_call_output items must produce the
// exact same upstream payload as the identical content expressed as
// function_call / function_call_output items, which is the shape verified to
// complete against gemini-4.x instead of stalling with zero bytes.
func TestAntigravityResponsesCustomToolItemsMatchFunctionItems(t *testing.T) {
	customPayload := []byte(`{
		"model": "gemini-4-argon-high",
		"stream": true,
		"reasoning": {"effort": "high", "summary": "auto"},
		"tools": [
			{"type": "custom", "name": "exec", "description": "run js"},
			{"type": "function", "name": "read_file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}
		],
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "id": "rs_1", "summary": []},
			{"type": "custom_tool_call", "call_id": "c1", "name": "exec", "input": "console.log('a')"},
			{"type": "custom_tool_call_output", "call_id": "c1", "output": "a` + "\n" + `"},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "go on"}]},
			{"type": "reasoning", "id": "rs_2", "summary": []},
			{"type": "custom_tool_call", "call_id": "c2", "name": "exec", "input": "console.log('b')"},
			{"type": "custom_tool_call_output", "call_id": "c2", "output": "b` + "\n" + `"}
		]
	}`)
	functionPayload := []byte(`{
		"model": "gemini-4-argon-high",
		"stream": true,
		"reasoning": {"effort": "high", "summary": "auto"},
		"tools": [
			{"type": "custom", "name": "exec", "description": "run js"},
			{"type": "function", "name": "read_file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}
		],
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "id": "rs_1", "summary": []},
			{"type": "function_call", "call_id": "c1", "name": "exec", "arguments": "{\"input\":\"console.log('a')\"}"},
			{"type": "function_call_output", "call_id": "c1", "output": "a` + "\n" + `"},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "go on"}]},
			{"type": "reasoning", "id": "rs_2", "summary": []},
			{"type": "function_call", "call_id": "c2", "name": "exec", "arguments": "{\"input\":\"console.log('b')\"}"},
			{"type": "function_call_output", "call_id": "c2", "output": "b` + "\n" + `"}
		]
	}`)

	customEnv := ConvertOpenAIResponsesRequestEnvelopeToAntigravity(nil, translator.RequestEnvelope{
		Model:  "gemini-4-argon-high",
		Body:   customPayload,
		Stream: true,
	})
	if customEnv.Err != nil {
		t.Fatalf("custom tool payload conversion failed: %v", customEnv.Err)
	}

	functionEnv := ConvertOpenAIResponsesRequestEnvelopeToAntigravity(nil, translator.RequestEnvelope{
		Model:  "gemini-4-argon-high",
		Body:   functionPayload,
		Stream: true,
	})
	if functionEnv.Err != nil {
		t.Fatalf("function tool payload conversion failed: %v", functionEnv.Err)
	}

	if string(customEnv.Body) != string(functionEnv.Body) {
		t.Fatalf("custom tool items must translate identically to the validated function_call rewrite through the Antigravity pipeline\ncustom:   %s\nfunction: %s", customEnv.Body, functionEnv.Body)
	}
}
