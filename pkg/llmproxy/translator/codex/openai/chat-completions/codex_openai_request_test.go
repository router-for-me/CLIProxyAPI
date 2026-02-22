package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToCodex(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`)

	got := ConvertOpenAIRequestToCodex("gpt-4o", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", res.Get("model").String())
	}

	if res.Get("reasoning.effort").String() != "medium" {
		t.Errorf("expected reasoning.effort medium, got %s", res.Get("reasoning.effort").String())
	}

	inputArray := res.Get("input").Array()
	if len(inputArray) != 1 {
		t.Errorf("expected 1 input item, got %d", len(inputArray))
	}

	// Test with image and tool calls
	input2 := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hi"}, {"type": "image_url", "image_url": {"url": "http://img"}}]},
			{"role": "assistant", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "f1", "arguments": "{}"}}]}
		],
		"tools": [{"type": "function", "function": {"name": "f1", "description": "d1", "parameters": {"type": "object"}}}],
		"reasoning_effort": "high"
	}`)

	got2 := ConvertOpenAIRequestToCodex("gpt-4o", input2, false)
	res2 := gjson.ParseBytes(got2)

	if res2.Get("reasoning.effort").String() != "high" {
		t.Errorf("expected reasoning.effort high, got %s", res2.Get("reasoning.effort").String())
	}

	inputArray2 := res2.Get("input").Array()
	// user message + assistant message (empty content) + function_call message
	if len(inputArray2) != 3 {
		t.Fatalf("expected 3 input items, got %d", len(inputArray2))
	}

	if inputArray2[2].Get("type").String() != "function_call" {
		t.Errorf("expected third input item to be function_call, got %s", inputArray2[2].Get("type").String())
	}
}

func TestConvertOpenAIRequestToCodex_NormalizesProxyPrefixedToolChoice(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "search_docs",
					"description": "search",
					"parameters": {"type": "object"}
				}
			}
		],
		"tool_choice": {
			"type": "function",
			"function": {"name": "proxy_search_docs"}
		}
	}`)

	got := ConvertOpenAIRequestToCodex("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if toolName := res.Get("tools.0.name").String(); toolName != "search_docs" {
		t.Fatalf("expected tools[0].name search_docs, got %s", toolName)
	}
	if choiceName := res.Get("tool_choice.name").String(); choiceName != "search_docs" {
		t.Fatalf("expected tool_choice.name search_docs, got %s", choiceName)
	}
}

func TestConvertOpenAIRequestToCodex_NormalizesProxyPrefixedAssistantToolCall(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "hello"},
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {"name": "proxy_search_docs", "arguments": "{}"}
					}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "search_docs",
					"description": "search",
					"parameters": {"type": "object"}
				}
			}
		]
	}`)

	got := ConvertOpenAIRequestToCodex("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if callName := res.Get("input.2.name").String(); callName != "search_docs" {
		t.Fatalf("expected function_call name search_docs, got %s", callName)
	}
}

func TestConvertOpenAIRequestToCodex_UsesVariantFallbackWhenReasoningEffortMissing(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"variant": "high"
	}`)

	got := ConvertOpenAIRequestToCodex("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if gotEffort := res.Get("reasoning.effort").String(); gotEffort != "high" {
		t.Fatalf("expected reasoning.effort to use variant fallback high, got %s", gotEffort)
	}
}

func TestConvertOpenAIRequestToCodex_UsesReasoningEffortBeforeVariant(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"reasoning_effort": "low",
		"variant": "high"
	}`)

	got := ConvertOpenAIRequestToCodex("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if gotEffort := res.Get("reasoning.effort").String(); gotEffort != "low" {
		t.Fatalf("expected reasoning.effort to prefer reasoning_effort low, got %s", gotEffort)
	}
}

func TestConvertOpenAIRequestToCodex_ResponseFormatMapsToTextFormat(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role":"user","content":"Return JSON"}],
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "answer",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {
						"result": {"type":"string"}
					},
					"required": ["result"]
				}
			}
		}
	}`)

	got := ConvertOpenAIRequestToCodex("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("response_format").Exists() {
		t.Fatalf("expected response_format to be removed from codex payload")
	}
	if gotType := res.Get("text.format.type").String(); gotType != "json_schema" {
		t.Fatalf("expected text.format.type json_schema, got %s", gotType)
	}
	if gotName := res.Get("text.format.name").String(); gotName != "answer" {
		t.Fatalf("expected text.format.name answer, got %s", gotName)
	}
	if gotStrict := res.Get("text.format.strict").Bool(); !gotStrict {
		t.Fatalf("expected text.format.strict true")
	}
	if !res.Get("text.format.schema").Exists() {
		t.Fatalf("expected text.format.schema to be present")
	}
}
