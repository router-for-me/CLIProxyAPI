package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses(t *testing.T) {
	ctx := context.Background()
	var param any

	// 1. First chunk (reasoning)
	chunk1 := []byte(`{"id": "resp1", "created": 123, "choices": [{"index": 0, "delta": {"reasoning_content": "Thinking..."}}]}`)
	got1 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk1, &param)
	// response.created, response.in_progress, response.output_item.added(rs), response.reasoning_summary_part.added, response.reasoning_summary_text.delta
	if len(got1) != 5 {
		t.Errorf("expected 5 events for first chunk, got %d", len(got1))
	}

	// 2. Second chunk (content)
	chunk2 := []byte(`{"id": "resp1", "choices": [{"index": 0, "delta": {"content": "Hello"}}]}`)
	got2 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk2, &param)
	// reasoning text.done, reasoning part.done, reasoning item.done, msg item.added, msg content.added, msg text.delta
	if len(got2) != 6 {
		t.Errorf("expected 6 events for second chunk, got %d", len(got2))
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(t *testing.T) {
	ctx := context.Background()
	rawJSON := []byte(`{
		"id": "chatcmpl-123",
		"created": 1677652288,
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello",
				"reasoning_content": "Think"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30
		}
	}`)

	got := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, "m1", nil, nil, rawJSON, nil)
	res := gjson.Parse(got)

	if res.Get("id").String() != "chatcmpl-123" {
		t.Errorf("expected id chatcmpl-123, got %s", res.Get("id").String())
	}

	outputs := res.Get("output").Array()
	if len(outputs) != 2 {
		t.Errorf("expected 2 output items, got %d", len(outputs))
	}

	if outputs[0].Get("type").String() != "reasoning" {
		t.Errorf("expected first output item reasoning, got %s", outputs[0].Get("type").String())
	}

	if outputs[1].Get("type").String() != "message" {
		t.Errorf("expected second output item message, got %s", outputs[1].Get("type").String())
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ToolCalls(t *testing.T) {
	ctx := context.Background()
	var param any

	// Start message
	chunk1 := []byte(`{"id": "resp1", "created": 123, "choices": [{"index": 0, "delta": {"content": "Hello"}}]}`)
	got1 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk1, &param)
	if len(got1) != 5 { // created, in_prog, item.added, content.added, text.delta
		t.Fatalf("expected 5 events, got %d", len(got1))
	}

	// Tool call delta (should trigger text done, part done, item done for current message)
	chunk2 := []byte(`{"id": "resp1", "choices": [{"index": 0, "delta": {"tool_calls": [{"id": "c1", "function": {"name": "f1", "arguments": "{}"}}]}}]}`)
	got2 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk2, &param)
	// text.done, content.done, item.done, tool_item.added, tool_args.delta
	if len(got2) != 5 {
		t.Errorf("expected 5 events for tool call, got %d", len(got2))
	}

	// Finish
	chunk3 := []byte(`{"id": "resp1", "choices": [{"index": 0, "finish_reason": "stop"}]}`)
	got3 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk3, &param)
	// tool_args.done, tool_item.done, completed
	if len(got3) != 3 {
		t.Errorf("expected 3 events for finish, got %d", len(got3))
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_Usage(t *testing.T) {
	ctx := context.Background()
	rawJSON := []byte(`{
		"id": "chatcmpl-123",
		"choices": [{"index": 0, "message": {"content": "hi"}}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"total_tokens": 15,
			"prompt_tokens_details": {"cached_tokens": 3},
			"output_tokens_details": {"reasoning_tokens": 2}
		}
	}`)

	got := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, "m1", nil, nil, rawJSON, nil)
	res := gjson.Parse(got)

	if res.Get("usage.input_tokens_details.cached_tokens").Int() != 3 {
		t.Errorf("expected cached_tokens 3, got %d", res.Get("usage.input_tokens_details.cached_tokens").Int())
	}
	if res.Get("usage.output_tokens_details.reasoning_tokens").Int() != 2 {
		t.Errorf("expected reasoning_tokens 2, got %d", res.Get("usage.output_tokens_details.reasoning_tokens").Int())
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DoneMarkerEmitsCompletion(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk := []byte(`{"id":"resp1","created":123,"choices":[{"index":0,"delta":{"content":"hello"}}]}`)
	_ = ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk, &param)

	doneEvents := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, []byte("[DONE]"), &param)
	if len(doneEvents) != 1 {
		t.Fatalf("expected exactly one event on [DONE], got %d", len(doneEvents))
	}
	if !strings.Contains(doneEvents[0], "event: response.completed") {
		t.Fatalf("expected response.completed event on [DONE], got %q", doneEvents[0])
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DoneMarkerNoDuplicateCompletion(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk1 := []byte(`{"id":"resp1","created":123,"choices":[{"index":0,"delta":{"content":"hello"}}]}`)
	_ = ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, chunk1, &param)

	finishChunk := []byte(`{"id":"resp1","choices":[{"index":0,"finish_reason":"stop"}]}`)
	finishEvents := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, finishChunk, &param)
	foundCompleted := false
	for _, event := range finishEvents {
		if strings.Contains(event, "event: response.completed") {
			foundCompleted = true
			break
		}
	}
	if !foundCompleted {
		t.Fatalf("expected response.completed on finish_reason chunk")
	}

	doneEvents := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "m1", nil, nil, []byte("[DONE]"), &param)
	if len(doneEvents) != 0 {
		t.Fatalf("expected no events on [DONE] after completion already emitted, got %d", len(doneEvents))
	}
}
