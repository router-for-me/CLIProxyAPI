package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaude(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)

	// Test streaming chunk with content
	chunk := []byte(`data: {"id": "chatcmpl-123", "model": "gpt-4o", "created": 1677652288, "choices": [{"index": 0, "delta": {"content": "Hello"}, "finish_reason": null}]}`)
	var param any
	got := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk, &param)

	if len(got) != 3 { // message_start + content_block_start + content_block_delta
		t.Errorf("expected 3 events, got %d", len(got))
	}

	// Test [DONE]
	doneChunk := []byte(`data: [DONE]`)
	gotDone := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, doneChunk, &param)
	if len(gotDone) == 0 {
		t.Errorf("expected events for [DONE], got 0")
	}
}

func TestConvertOpenAIResponseToClaude_DoneWithoutDataPrefix(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	chunk := []byte(`data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello"}}]}`)
	_ = ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk, &param)

	doneChunk := []byte(`[DONE]`)
	got := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, doneChunk, &param)
	if len(got) == 0 {
		t.Fatalf("expected terminal events for bare [DONE], got 0")
	}

	last := got[len(got)-1]
	if !strings.Contains(last, `"type":"message_stop"`) {
		t.Fatalf("expected final message_stop event, got %q", last)
	}
}

func TestConvertOpenAIResponseToClaude_DoneWithoutDataPrefixEmitsMessageDeltaAfterFinishReason(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	chunk := []byte(`data: {"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	gotFinish := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk, &param)
	if len(gotFinish) == 0 {
		t.Fatalf("expected finish chunk events, got 0")
	}

	doneChunk := []byte(`[DONE]`)
	gotDone := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, doneChunk, &param)
	if len(gotDone) < 2 {
		t.Fatalf("expected message_delta and message_stop on bare [DONE], got %d events", len(gotDone))
	}
	if !strings.Contains(gotDone[0], `"type":"message_delta"`) {
		t.Fatalf("expected first event message_delta, got %q", gotDone[0])
	}
	if !strings.Contains(gotDone[len(gotDone)-1], `"type":"message_stop"`) {
		t.Fatalf("expected last event message_stop, got %q", gotDone[len(gotDone)-1])
	}
}

func TestConvertOpenAIResponseToClaude_StreamingReasoning(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	// 1. Reasoning content chunk
	chunk1 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"reasoning_content": "Thinking..."}}]}`)
	got1 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk1, &param)
	// message_start + content_block_start(thinking) + content_block_delta(thinking)
	if len(got1) != 3 {
		t.Errorf("expected 3 events, got %d", len(got1))
	}

	// 2. Transition to content
	chunk2 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"content": "Hello"}}]}`)
	got2 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk2, &param)
	_ = got2
	// content_block_stop(thinking) + content_block_start(text) + content_block_delta(text)
	if len(got2) != 3 {
		t.Errorf("expected 3 events for transition, got %d", len(got2))
	}
}

func TestConvertOpenAIResponseToClaude_StreamingToolCalls(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": true}`)
	request := []byte(`{}`)
	var param any

	// 1. Tool call chunk (start)
	chunk1 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"tool_calls": [{"index": 0, "id": "call_1", "function": {"name": "my_tool", "arguments": ""}}]}}]}`)
	got1 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk1, &param)
	// message_start + content_block_start(tool_use)
	if len(got1) != 2 {
		t.Errorf("expected 2 events, got %d", len(got1))
	}

	// 2. Tool call chunk (arguments)
	chunk2 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"tool_calls": [{"index": 0, "function": {"arguments": "{\"a\":1}"}}]}}]}`)
	got2 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk2, &param)
	_ = got2
	// No events emitted during argument accumulation usually, wait until stop or [DONE]
	// Actually, the current implementation emits nothing for arguments during accumulation.

	// 3. Finish reason tool_calls
	chunk3 := []byte(`data: {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]}`)
	got3 := ConvertOpenAIResponseToClaude(ctx, "claude-3-sonnet", originalRequest, request, chunk3, &param)
	// content_block_delta(input_json_delta) + content_block_stop
	if len(got3) != 2 {
		t.Errorf("expected 2 events for finish, got %d", len(got3))
	}
}

func TestConvertOpenAIResponseToClaudeNonStream(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": false}`)
	request := []byte(`{}`)

	// Test non-streaming response with reasoning and content
	response := []byte(`{
		"id": "chatcmpl-123",
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello",
				"reasoning_content": "Thinking..."
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20
		}
	}`)

	got := ConvertOpenAIResponseToClaudeNonStream(ctx, "claude-3-sonnet", originalRequest, request, response, nil)
	res := gjson.Parse(got)

	if res.Get("id").String() != "chatcmpl-123" {
		t.Errorf("expected id chatcmpl-123, got %s", res.Get("id").String())
	}

	content := res.Get("content").Array()
	if len(content) != 2 {
		t.Errorf("expected 2 content blocks, got %d", len(content))
	}

	if content[0].Get("type").String() != "thinking" {
		t.Errorf("expected first block type thinking, got %s", content[0].Get("type").String())
	}

	if content[1].Get("type").String() != "text" {
		t.Errorf("expected second block type text, got %s", content[1].Get("type").String())
	}
}

func TestConvertOpenAIResponseToClaude_ToolCalls(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream": false}`)
	request := []byte(`{}`)

	response := []byte(`{
		"id": "chatcmpl-123",
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "my_tool",
						"arguments": "{\"arg\": 1}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`)

	got := ConvertOpenAIResponseToClaudeNonStream(ctx, "claude-3-sonnet", originalRequest, request, response, nil)
	res := gjson.Parse(got)

	content := res.Get("content").Array()
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}

	if content[0].Get("type").String() != "tool_use" {
		t.Errorf("expected tool_use block, got %s", content[0].Get("type").String())
	}

	if content[0].Get("name").String() != "my_tool" {
		t.Errorf("expected tool name my_tool, got %s", content[0].Get("name").String())
	}
}
