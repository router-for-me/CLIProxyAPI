package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "hello"}
				]
			}
		],
		"max_output_tokens": 100
	}`)

	got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o-new", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o-new" {
		t.Errorf("expected model gpt-4o-new, got %s", res.Get("model").String())
	}

	if res.Get("stream").Bool() != true {
		t.Errorf("expected stream true, got %v", res.Get("stream").Bool())
	}

	if res.Get("max_tokens").Int() != 100 {
		t.Errorf("expected max_tokens 100, got %d", res.Get("max_tokens").Int())
	}

	messages := res.Get("messages").Array()
	if len(messages) != 2 {
		t.Errorf("expected 2 messages (system + user), got %d", len(messages))
	}

	if messages[0].Get("role").String() != "system" || messages[0].Get("content").String() != "Be helpful." {
		t.Errorf("unexpected system message: %s", messages[0].Raw)
	}

	if messages[1].Get("role").String() != "user" || messages[1].Get("content.0.text").String() != "hello" {
		t.Errorf("unexpected user message: %s", messages[1].Raw)
	}

	// Test full input with messages, function calls, and results
	input2 := []byte(`{
		"instructions": "sys",
		"input": [
			{"role": "user", "content": "hello"},
			{"type": "function_call", "call_id": "c1", "name": "f1", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": "ok"}
		],
		"tools": [{"type": "function", "name": "f1", "description": "d1", "parameters": {"type": "object"}}],
		"max_output_tokens": 100,
		"reasoning": {"effort": "high"}
	}`)

	got2 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("m1", input2, false)
	res2 := gjson.ParseBytes(got2)

	if res2.Get("max_tokens").Int() != 100 {
		t.Errorf("expected max_tokens 100, got %d", res2.Get("max_tokens").Int())
	}

	if res2.Get("reasoning_effort").String() != "high" {
		t.Errorf("expected reasoning_effort high, got %s", res2.Get("reasoning_effort").String())
	}

	messages2 := res2.Get("messages").Array()
	// sys + user + assistant(tool_call) + tool(result)
	if len(messages2) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages2))
	}

	if messages2[2].Get("role").String() != "assistant" || !messages2[2].Get("tool_calls").Exists() {
		t.Error("expected third message to be assistant with tool_calls")
	}

	if messages2[3].Get("role").String() != "tool" || messages2[3].Get("content").String() != "ok" {
		t.Error("expected fourth message to be tool with content ok")
	}

	if len(res2.Get("tools").Array()) != 1 {
		t.Errorf("expected 1 tool, got %d", len(res2.Get("tools").Array()))
	}

	// Test with developer role, image, and parallel tool calls
	input3 := []byte(`{
		"model": "gpt-4o",
		"input": [
			{"role": "developer", "content": "dev msg"},
			{"role": "user", "content": [{"type": "input_image", "image_url": "http://img"}]}
		],
		"parallel_tool_calls": true
	}`)
	got3 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", input3, false)
	res3 := gjson.ParseBytes(got3)

	messages3 := res3.Get("messages").Array()
	if len(messages3) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages3))
	}
	// developer -> user
	if messages3[0].Get("role").String() != "user" {
		t.Errorf("expected developer role converted to user, got %s", messages3[0].Get("role").String())
	}
	// image content
	if messages3[1].Get("content.0.type").String() != "image_url" {
		t.Errorf("expected image_url type, got %s", messages3[1].Get("content.0.type").String())
	}
	if res3.Get("parallel_tool_calls").Bool() != true {
		t.Error("expected parallel_tool_calls true")
	}

	// Test input as string
	input4 := []byte(`{"input": "hello"}`)
	got4 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", input4, false)
	res4 := gjson.ParseBytes(got4)
	if res4.Get("messages.0.content").String() != "hello" {
		t.Errorf("expected content hello, got %s", res4.Get("messages.0.content").String())
	}
}
