package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConvertCodexRequestToOpenAI_Basic(t *testing.T) {
	input := []byte(`{
		"model": "kimi-for-coding",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
		],
		"max_tokens": 100
	}`)

	output := ConvertCodexRequestToOpenAI("kimi-for-coding", input, false)

	var result map[string]interface{}
	err := json.Unmarshal(output, &result)
	assert.NoError(t, err)

	assert.Equal(t, "kimi-for-coding", result["model"])
	assert.Equal(t, false, result["stream"])
	assert.Equal(t, float64(100), result["max_tokens"])

	messages, ok := result["messages"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, messages, 1)

	msg := messages[0].(map[string]interface{})
	assert.Equal(t, "user", msg["role"])
	assert.Equal(t, "Hello", msg["content"])
}

func TestConvertCodexRequestToOpenAI_WithTools(t *testing.T) {
	input := []byte(`{
		"model": "kimi-for-coding",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "Test"}]}
		],
		"tools": [
			{
				"type": "function",
				"name": "test.function",
				"description": "Test function",
				"parameters": {"type": "object", "properties": {"query": {"type": "string"}}}
			}
		]
	}`)

	output := ConvertCodexRequestToOpenAI("kimi-for-coding", input, false)

	var result map[string]interface{}
	err := json.Unmarshal(output, &result)
	assert.NoError(t, err)

	tools, ok := result["tools"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, tools, 1)

	tool := tools[0].(map[string]interface{})
	assert.Equal(t, "function", tool["type"])

	fn := tool["function"].(map[string]interface{})
	assert.Equal(t, "test_function", fn["name"])
	assert.Equal(t, "Test function", fn["description"])
}

func TestConvertCodexRequestToOpenAI_FunctionCall(t *testing.T) {
	input := []byte(`{
		"model": "kimi-for-coding",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "Call function"}]},
			{"type": "function_call", "call_id": "call_123", "name": "test_function", "arguments": "{\"query\":\"test\"}"},
			{"type": "function_call_output", "call_id": "call_123", "output": "result"}
		]
	}`)

	output := ConvertCodexRequestToOpenAI("kimi-for-coding", input, false)

	var result map[string]interface{}
	err := json.Unmarshal(output, &result)
	assert.NoError(t, err)

	messages, ok := result["messages"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, messages, 3)

	// Check assistant message with tool_calls
	msg1 := messages[1].(map[string]interface{})
	assert.Equal(t, "assistant", msg1["role"])
	assert.Nil(t, msg1["content"])

	toolCalls, ok := msg1["tool_calls"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, toolCalls, 1)

	// Check tool message
	msg2 := messages[2].(map[string]interface{})
	assert.Equal(t, "tool", msg2["role"])
	assert.Equal(t, "call_123", msg2["tool_call_id"])
	assert.Equal(t, "result", msg2["content"])
}

func TestConvertCodexRequestToOpenAI_DeveloperRole(t *testing.T) {
	input := []byte(`{
		"model": "kimi-for-coding",
		"input": [
			{"role": "developer", "content": [{"type": "input_text", "text": "System prompt"}]}
		]
	}`)

	output := ConvertCodexRequestToOpenAI("kimi-for-coding", input, false)

	var result map[string]interface{}
	err := json.Unmarshal(output, &result)
	assert.NoError(t, err)

	messages := result["messages"].([]interface{})
	msg := messages[0].(map[string]interface{})
	assert.Equal(t, "system", msg["role"])
}

func TestNormalizeFunctionName(t *testing.T) {
	assert.Equal(t, "test_function", normalizeFunctionName("test.function"))
	assert.Equal(t, "my_test_func", normalizeFunctionName("my.test.func"))
	assert.Equal(t, "no_dots", normalizeFunctionName("no_dots"))
}

func TestConvertOpenAIResponseToCodexNonStream(t *testing.T) {
	input := []byte(`{
		"id": "chatcmpl-xxx",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "kimi-for-coding",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Hello!"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 44,
			"completion_tokens": 52,
			"total_tokens": 96
		}
	}`)

	originalRequest := []byte(`{"tools":[{"type":"function","name":"test.function"}]}`)
	output := ConvertOpenAIResponseToCodexNonStream(context.Background(), "kimi-for-coding", originalRequest, nil, input, nil)

	var result map[string]interface{}
	err := json.Unmarshal(output, &result)
	assert.NoError(t, err)

	assert.Contains(t, result["id"], "chatcmpl-xxx")
	assert.Equal(t, "kimi-for-coding", result["model"])

	outputArr, ok := result["output"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, outputArr, 1)

	msg := outputArr[0].(map[string]interface{})
	assert.Equal(t, "message", msg["type"])
	assert.Equal(t, "assistant", msg["role"])
}

func TestConvertOpenAIResponseToCodexNonStream_WithToolCalls(t *testing.T) {
	input := []byte(`{
		"id": "chatcmpl-xxx",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "kimi-for-coding",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_xxx",
					"type": "function",
					"function": {
						"name": "test_function",
						"arguments": "{\"query\":\"test\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 44,
			"completion_tokens": 52,
			"total_tokens": 96
		}
	}`)

	originalRequest := []byte(`{"tools":[{"type":"function","name":"test.function"}]}`)
	output := ConvertOpenAIResponseToCodexNonStream(context.Background(), "kimi-for-coding", originalRequest, nil, input, nil)

	var result map[string]interface{}
	err := json.Unmarshal(output, &result)
	assert.NoError(t, err)

	outputArr := result["output"].([]interface{})
	assert.Len(t, outputArr, 1)

	fc := outputArr[0].(map[string]interface{})
	assert.Equal(t, "function_call", fc["type"])
	assert.Equal(t, "call_xxx", fc["call_id"])
	// Original name should be restored
	assert.Equal(t, "test.function", fc["name"])
	assert.Equal(t, "{\"query\":\"test\"}", fc["arguments"])
}
