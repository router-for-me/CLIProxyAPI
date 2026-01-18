package responses

import (
	"context"
	"strings"
	"testing"
)

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FinalizesOnDoneWithoutFinishReason(t *testing.T) {
	ctx := context.Background()
	model := "glm-4.7"

	var param any
	request := []byte(`{"model":"glm-4.7"}`)

	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1730000000,"choices":[{"index":0,"delta":{"reasoning_content":"hello"}}]}`),
		[]byte(`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1730000000,"choices":[{"index":0,"delta":{"reasoning_content":" world"}}]}`),
		[]byte("data: [DONE]"),
	}

	var events []string
	for _, c := range chunks {
		events = append(events, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, nil, request, c, &param)...)
	}

	joined := strings.Join(events, "\n")
	if !strings.Contains(joined, "event: response.completed") {
		t.Fatalf("expected response.completed event, got %d events\n%s", len(events), joined)
	}
	if !strings.Contains(joined, "event: response.reasoning_summary_text.done") {
		t.Fatalf("expected reasoning_summary_text.done event, got %d events\n%s", len(events), joined)
	}
	if !strings.Contains(joined, "event: response.reasoning_summary_part.done") {
		t.Fatalf("expected reasoning_summary_part.done event, got %d events\n%s", len(events), joined)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FinalizesTextOnDoneWithoutFinishReason(t *testing.T) {
	ctx := context.Background()
	model := "glm-4.7"

	var param any
	request := []byte(`{"model":"glm-4.7"}`)

	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1730000000,"choices":[{"index":0,"delta":{"content":"Hello"}}]}`),
		[]byte(`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1730000000,"choices":[{"index":0,"delta":{"content":" world"}}]}`),
		[]byte("data: [DONE]"),
	}

	var events []string
	for _, c := range chunks {
		events = append(events, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, nil, request, c, &param)...)
	}

	joined := strings.Join(events, "\n")
	if !strings.Contains(joined, "event: response.completed") {
		t.Fatalf("expected response.completed event, got %d events\n%s", len(events), joined)
	}
	if !strings.Contains(joined, "event: response.output_text.done") {
		t.Fatalf("expected output_text.done event, got %d events\n%s", len(events), joined)
	}
	if !strings.Contains(joined, "event: response.content_part.done") {
		t.Fatalf("expected content_part.done event, got %d events\n%s", len(events), joined)
	}
}
