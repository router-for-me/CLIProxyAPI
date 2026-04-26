package executor

import "testing"

func TestObserveOpenAICompatToolStreamChunk_TracksToolCallAndFinishReason(t *testing.T) {
	observation := &openAICompatToolObservation{}

	observeOpenAICompatToolStreamChunk([]byte(`data: {"id":"chatcmpl_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":""}}]},"finish_reason":null}]}`), 3, observation)
	observeOpenAICompatToolStreamChunk([]byte(`data: {"id":"chatcmpl_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), 5, observation)

	if !observation.sawToolCalls {
		t.Fatal("expected sawToolCalls to be true")
	}
	if observation.firstToolCallChunk != 3 {
		t.Fatalf("firstToolCallChunk = %d, want 3", observation.firstToolCallChunk)
	}
	if observation.toolCallDeltaCount != 1 {
		t.Fatalf("toolCallDeltaCount = %d, want 1", observation.toolCallDeltaCount)
	}
	if observation.lastFinishReason != "tool_calls" {
		t.Fatalf("lastFinishReason = %q, want %q", observation.lastFinishReason, "tool_calls")
	}
	if observation.finishReasonChunk != 5 {
		t.Fatalf("finishReasonChunk = %d, want 5", observation.finishReasonChunk)
	}
	if !observation.finishReasonToolCall {
		t.Fatal("expected finishReasonToolCall to be true")
	}
}

func TestObserveOpenAICompatToolStreamChunk_IgnoresPlainTextOnlyStream(t *testing.T) {
	observation := &openAICompatToolObservation{}

	observeOpenAICompatToolStreamChunk([]byte(`data: {"id":"chatcmpl_2","choices":[{"index":0,"delta":{"content":"Let me invoke Write properly now"},"finish_reason":null}]}`), 2, observation)
	observeOpenAICompatToolStreamChunk([]byte(`data: {"id":"chatcmpl_2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), 4, observation)

	if observation.sawToolCalls {
		t.Fatal("expected sawToolCalls to remain false")
	}
	if observation.firstToolCallChunk != 0 {
		t.Fatalf("firstToolCallChunk = %d, want 0", observation.firstToolCallChunk)
	}
	if observation.toolCallDeltaCount != 0 {
		t.Fatalf("toolCallDeltaCount = %d, want 0", observation.toolCallDeltaCount)
	}
	if observation.lastFinishReason != "stop" {
		t.Fatalf("lastFinishReason = %q, want %q", observation.lastFinishReason, "stop")
	}
	if observation.finishReasonToolCall {
		t.Fatal("expected finishReasonToolCall to remain false")
	}
}

func TestObserveOpenAICompatToolStreamChunk_TracksLegacyFunctionCallDeltas(t *testing.T) {
	observation := &openAICompatToolObservation{}

	observeOpenAICompatToolStreamChunk([]byte(`data: {"id":"chatcmpl_legacy","choices":[{"index":0,"delta":{"function_call":{"name":"write","arguments":"{"}},"finish_reason":null}]}`), 2, observation)
	observeOpenAICompatToolStreamChunk([]byte(`data: {"id":"chatcmpl_legacy","choices":[{"index":0,"delta":{},"finish_reason":"function_call"}]}`), 4, observation)

	if !observation.sawToolCalls {
		t.Fatal("expected legacy function_call delta to count as tool call")
	}
	if observation.firstToolCallChunk != 2 {
		t.Fatalf("firstToolCallChunk = %d, want 2", observation.firstToolCallChunk)
	}
	if observation.toolCallDeltaCount != 1 {
		t.Fatalf("toolCallDeltaCount = %d, want 1", observation.toolCallDeltaCount)
	}
	if observation.lastFinishReason != "function_call" {
		t.Fatalf("lastFinishReason = %q, want %q", observation.lastFinishReason, "function_call")
	}
	if !observation.finishReasonToolCall {
		t.Fatal("expected finishReasonToolCall to be true for function_call")
	}
}
