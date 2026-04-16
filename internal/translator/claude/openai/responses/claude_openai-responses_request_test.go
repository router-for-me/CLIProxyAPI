package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_InputArray(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M2.7","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":false}`)

	out := ConvertOpenAIResponsesRequestToClaude("MiniMax-M2.7", input, false)
	result := gjson.ParseBytes(out)
	messages := result.Get("messages").Array()

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %s", len(messages), result.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("expected user role, got %q", got)
	}
	if got := messages[0].Get("content").String(); got != "hello" {
		t.Fatalf("expected content hello, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_InputString(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M2.7","input":"hello from string","stream":false}`)

	out := ConvertOpenAIResponsesRequestToClaude("MiniMax-M2.7", input, false)
	result := gjson.ParseBytes(out)
	messages := result.Get("messages").Array()

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %s", len(messages), result.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("expected user role, got %q", got)
	}
	if got := messages[0].Get("content").String(); got != "hello from string" {
		t.Fatalf("expected string content to be preserved, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_InputObjectMessage(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M2.7","input":{"role":"user","content":[{"type":"input_text","text":"hello from object"}]},"stream":false}`)

	out := ConvertOpenAIResponsesRequestToClaude("MiniMax-M2.7", input, false)
	result := gjson.ParseBytes(out)
	messages := result.Get("messages").Array()

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %s", len(messages), result.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("expected user role, got %q", got)
	}
	if got := messages[0].Get("content").String(); got != "hello from object" {
		t.Fatalf("expected object message content to be preserved, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_InputObjectFunctionCall(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M2.7","input":{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"city\":\"Shanghai\"}"},"stream":false}`)

	out := ConvertOpenAIResponsesRequestToClaude("MiniMax-M2.7", input, false)
	result := gjson.ParseBytes(out)
	messages := result.Get("messages").Array()

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %s", len(messages), result.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "assistant" {
		t.Fatalf("expected assistant role, got %q", got)
	}
	if got := messages[0].Get("content.0.type").String(); got != "tool_use" {
		t.Fatalf("expected tool_use content, got %q", got)
	}
	if got := messages[0].Get("content.0.id").String(); got != "call_123" {
		t.Fatalf("expected tool id call_123, got %q", got)
	}
	if got := messages[0].Get("content.0.name").String(); got != "get_weather" {
		t.Fatalf("expected tool name get_weather, got %q", got)
	}
	if got := messages[0].Get("content.0.input.city").String(); got != "Shanghai" {
		t.Fatalf("expected tool args to be preserved, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_InputObjectFunctionCallOutput(t *testing.T) {
	input := []byte(`{"model":"MiniMax-M2.7","input":{"type":"function_call_output","call_id":"call_123","output":"sunny"},"stream":false}`)

	out := ConvertOpenAIResponsesRequestToClaude("MiniMax-M2.7", input, false)
	result := gjson.ParseBytes(out)
	messages := result.Get("messages").Array()

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d: %s", len(messages), result.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("expected user role, got %q", got)
	}
	if got := messages[0].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("expected tool_result content, got %q", got)
	}
	if got := messages[0].Get("content.0.tool_use_id").String(); got != "call_123" {
		t.Fatalf("expected tool_use_id call_123, got %q", got)
	}
	if got := messages[0].Get("content.0.content").String(); got != "sunny" {
		t.Fatalf("expected tool output sunny, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_CleansToolSchema(t *testing.T) {
	input := []byte(`{
		"model":"MiniMax-M2.7",
		"input":"hello",
		"tools":[
			{
				"type":"function",
				"name":"sessions_list",
				"description":"list",
				"parameters":{
					"type":"object",
					"properties":{
						"sessions":{"type":"array","items":null}
					},
					"required":null
				}
			}
		],
		"stream":false
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("MiniMax-M2.7", input, false)
	result := gjson.ParseBytes(out)

	if got := result.Get("tools.0.input_schema.properties.sessions.items.type").String(); got == "" {
		t.Fatalf("expected sessions.items.type to be normalized: %s", result.Get("tools").Raw)
	}
	if result.Get("tools.0.input_schema.required").Exists() {
		t.Fatalf("required should be removed when null: %s", result.Get("tools").Raw)
	}
}
