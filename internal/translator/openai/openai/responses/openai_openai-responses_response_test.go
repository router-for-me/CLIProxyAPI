package responses

import (
	"context"
	"strings"
	"testing"
)

func TestConvertOpenAIStream_DONEOnly_EmitsCompletion(t *testing.T) {
	ctx := context.Background()
	var param any
	req := []byte(`{"model":"kimi-for-coding"}`)

	// No prior chunks; only [DONE]. Should emit response.completed (possibly with empty output).
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "kimi", nil, req, []byte("[DONE]"), &param)
	var hasCompleted bool
	for _, s := range out {
		if strings.Contains(s, "response.completed") {
			hasCompleted = true
			break
		}
	}
	if !hasCompleted {
		t.Errorf("stream with only [DONE]: expected at least one response.completed event, got %d events", len(out))
	}
}

func TestConvertOpenAIStream_DeltasThenDONE_EmitsCompletionWithOutput(t *testing.T) {
	ctx := context.Background()
	var param any
	req := []byte(`{"model":"kimi-for-coding"}`)

	// Chunk 1: create + content delta (no finish_reason)
	ch1 := []byte(`data: {"id":"ch-1","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"content":"hi"}}]}`)
	out1 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "kimi", nil, req, ch1, &param)
	if len(out1) == 0 {
		t.Fatal("expected events from content delta chunk")
	}

	// Chunk 2: [DONE] only. Should emit completion (we never saw finish_reason).
	out2 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "kimi", nil, req, []byte("[DONE]"), &param)
	var hasCompleted bool
	var completedData string
	for _, s := range out2 {
		if strings.Contains(s, "response.completed") {
			hasCompleted = true
			completedData = s
			break
		}
	}
	if !hasCompleted {
		t.Fatalf("stream [deltas then DONE]: expected response.completed, got %d events", len(out2))
	}
	if !strings.Contains(completedData, "hi") {
		t.Errorf("response.completed should contain accumulated content %q", "hi")
	}
}

func TestConvertOpenAIStream_FinishReasonThenDONE_OneCompletionOnly(t *testing.T) {
	ctx := context.Background()
	var param any
	req := []byte(`{"model":"kimi-for-coding"}`)

	// Chunk 1: content + finish_reason
	ch1 := []byte(`data: {"id":"ch-1","object":"chat.completion.chunk","created":1,"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	out1 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "kimi", nil, req, ch1, &param)
	n1 := 0
	for _, s := range out1 {
		if strings.Contains(s, "response.completed") {
			n1++
		}
	}
	if n1 != 1 {
		t.Fatalf("after finish_reason chunk: expected exactly 1 response.completed, got %d", n1)
	}

	// Chunk 2: [DONE]. Should emit nothing (already completed).
	out2 := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "kimi", nil, req, []byte("[DONE]"), &param)
	if len(out2) != 0 {
		t.Fatalf("after [DONE] when already completed: expected 0 events, got %d", len(out2))
	}
}
