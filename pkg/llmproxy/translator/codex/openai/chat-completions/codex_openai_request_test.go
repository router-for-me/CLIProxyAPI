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
