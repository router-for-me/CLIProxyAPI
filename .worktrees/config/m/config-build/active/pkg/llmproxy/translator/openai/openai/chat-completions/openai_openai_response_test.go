package chat_completions

import (
	"context"
	"testing"
)

func TestConvertOpenAIResponseToOpenAI(t *testing.T) {
	ctx := context.Background()
	rawJSON := []byte(`data: {"id": "123"}`)
	got := ConvertOpenAIResponseToOpenAI(ctx, "model", nil, nil, rawJSON, nil)
	if len(got) != 1 || got[0] != `{"id": "123"}` {
		t.Errorf("expected {\"id\": \"123\"}, got %v", got)
	}

	doneJSON := []byte(`data: [DONE]`)
	gotDone := ConvertOpenAIResponseToOpenAI(ctx, "model", nil, nil, doneJSON, nil)
	if len(gotDone) != 0 {
		t.Errorf("expected empty slice for [DONE], got %v", gotDone)
	}
}

func TestConvertOpenAIResponseToOpenAINonStream(t *testing.T) {
	ctx := context.Background()
	rawJSON := []byte(`{"id": "123"}`)
	got := ConvertOpenAIResponseToOpenAINonStream(ctx, "model", nil, nil, rawJSON, nil)
	if got != `{"id": "123"}` {
		t.Errorf("expected {\"id\": \"123\"}, got %s", got)
	}
}
