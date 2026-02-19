package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToCodex_DisablesParallelToolCallsWhenStrict(t *testing.T) {
	input := []byte(`{
		"messages":[{"role":"user","content":"Return JSON"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"result",
				"strict":true,
				"schema":{
					"type":"object",
					"properties":{"answer":{"type":"string"}},
					"required":["answer"],
					"additionalProperties":false
				}
			}
		},
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"save_answer",
					"description":"save",
					"strict":true,
					"parameters":{"type":"object","properties":{}}
				}
			}
		]
	}`)

	output := ConvertOpenAIRequestToCodex("gpt-5", input, false)
	raw := string(output)

	if !gjson.Get(raw, "text.format.strict").Bool() {
		t.Fatalf("expected text.format.strict=true, got %s", gjson.Get(raw, "text.format.strict").Raw)
	}
	if gjson.Get(raw, "parallel_tool_calls").Bool() {
		t.Fatalf("expected parallel_tool_calls=false when strict is enabled, got %s", gjson.Get(raw, "parallel_tool_calls").Raw)
	}
}

func TestConvertOpenAIRequestToCodex_UsesParallelToolCallsWhenNotStrict(t *testing.T) {
	input := []byte(`{
		"messages":[{"role":"user","content":"Hello"}],
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"save_answer",
					"description":"save",
					"strict":false,
					"parameters":{"type":"object","properties":{}}
				}
			}
		]
	}`)

	output := ConvertOpenAIRequestToCodex("gpt-5", input, false)
	raw := string(output)

	if !gjson.Get(raw, "parallel_tool_calls").Bool() {
		t.Fatalf("expected parallel_tool_calls=true when strict is not enabled, got %s", gjson.Get(raw, "parallel_tool_calls").Raw)
	}
}
