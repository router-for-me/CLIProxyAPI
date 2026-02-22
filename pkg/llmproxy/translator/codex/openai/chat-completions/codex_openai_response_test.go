package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAI(t *testing.T) {
	ctx := context.Background()
	var param any

	// response.created
	raw := []byte(`data: {"type": "response.created", "response": {"id": "resp_123", "created_at": 1629141600, "model": "gpt-4o"}}`)
	got := ConvertCodexResponseToOpenAI(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 0 {
		t.Errorf("expected 0 chunks for response.created, got %d", len(got))
	}

	// response.output_text.delta
	raw = []byte(`data: {"type": "response.output_text.delta", "delta": "hello"}`)
	got = ConvertCodexResponseToOpenAI(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	res := gjson.Parse(got[0])
	if res.Get("id").String() != "resp_123" || res.Get("choices.0.delta.content").String() != "hello" {
		t.Errorf("unexpected output: %s", got[0])
	}

	// response.reasoning_summary_text.delta
	raw = []byte(`data: {"type": "response.reasoning_summary_text.delta", "delta": "Thinking..."}`)
	got = ConvertCodexResponseToOpenAI(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk for reasoning, got %d", len(got))
	}
	res = gjson.Parse(got[0])
	if res.Get("choices.0.delta.reasoning_content").String() != "Thinking..." {
		t.Errorf("expected reasoning_content Thinking..., got %s", res.Get("choices.0.delta.reasoning_content").String())
	}

	// response.output_item.done (function_call)
	raw = []byte(`data: {"type": "response.output_item.done", "item": {"type": "function_call", "call_id": "c1", "name": "f1", "arguments": "{}"}}`)
	got = ConvertCodexResponseToOpenAI(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk for tool call, got %d", len(got))
	}
	res = gjson.Parse(got[0])
	if res.Get("choices.0.delta.tool_calls.0.function.name").String() != "f1" {
		t.Errorf("expected function name f1, got %s", res.Get("choices.0.delta.tool_calls.0.function.name").String())
	}
}

func TestConvertCodexResponseToOpenAINonStream(t *testing.T) {
	raw := []byte(`{"type": "response.completed", "response": {
		"id": "resp_123",
		"model": "gpt-4o",
		"created_at": 1629141600,
		"output": [
			{"type": "message", "content": [
				{"type": "output_text", "text": "hello"}
			]}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5},
		"status": "completed"
	}}`)

	got := ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-4o", nil, nil, raw, nil)
	res := gjson.Parse(got)
	if res.Get("id").String() != "resp_123" {
		t.Errorf("expected id resp_123, got %s", res.Get("id").String())
	}
	if res.Get("choices.0.message.content").String() != "hello" {
		t.Errorf("unexpected content: %s", got)
	}
}

func TestConvertCodexResponseToOpenAINonStream_Full(t *testing.T) {
	raw := []byte(`{"type": "response.completed", "response": {
		"id": "resp_123",
		"model": "gpt-4o",
		"created_at": 1629141600,
		"status": "completed",
		"output": [
			{
				"type": "reasoning",
				"summary": [{"type": "summary_text", "text": "thought"}]
			},
			{
				"type": "message",
				"content": [{"type": "output_text", "text": "result"}]
			},
			{
				"type": "function_call",
				"call_id": "c1",
				"name": "f1",
				"arguments": "{}"
			}
		],
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5,
			"total_tokens": 15,
			"output_tokens_details": {"reasoning_tokens": 2}
		}
	}}`)

	got := ConvertCodexResponseToOpenAINonStream(context.Background(), "gpt-4o", nil, nil, raw, nil)
	res := gjson.Parse(got)

	if res.Get("choices.0.message.reasoning_content").String() != "thought" {
		t.Errorf("expected reasoning_content thought, got %s", res.Get("choices.0.message.reasoning_content").String())
	}
	if res.Get("choices.0.message.content").String() != "result" {
		t.Errorf("expected content result, got %s", res.Get("choices.0.message.content").String())
	}
	if res.Get("choices.0.message.tool_calls.0.function.name").String() != "f1" {
		t.Errorf("expected tool call f1, got %s", res.Get("choices.0.message.tool_calls.0.function.name").String())
	}
	if res.Get("choices.0.finish_reason").String() != "tool_calls" {
		t.Errorf("expected finish_reason tool_calls, got %s", res.Get("choices.0.finish_reason").String())
	}
	if res.Get("usage.completion_tokens_details.reasoning_tokens").Int() != 2 {
		t.Errorf("expected reasoning_tokens 2, got %d", res.Get("usage.completion_tokens_details.reasoning_tokens").Int())
	}
}
