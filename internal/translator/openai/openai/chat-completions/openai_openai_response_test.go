package chat_completions

import (
	"bytes"
	"context"
	"testing"
)

func TestConvertOpenAIResponseToOpenAIDropsChunksAfterDone(t *testing.T) {
	var param any
	ctx := context.Background()

	first := ConvertOpenAIResponseToOpenAI(ctx, "m", nil, nil, []byte(`data: {"id":"x","choices":[]}`), &param)
	if len(first) != 1 || !bytes.Contains(first[0], []byte(`"id":"x"`)) {
		t.Fatalf("first chunk = %v", first)
	}

	done := ConvertOpenAIResponseToOpenAI(ctx, "m", nil, nil, []byte("data: [DONE]"), &param)
	if len(done) != 0 {
		t.Fatalf("DONE should yield no output, got %v", done)
	}
	if doneFlag, ok := param.(bool); !ok || !doneFlag {
		t.Fatalf("param after DONE = %#v, want true", param)
	}

	trailing := ConvertOpenAIResponseToOpenAI(ctx, "m", nil, nil, []byte(`data: {"choices":[],"cost":"0"}`), &param)
	if len(trailing) != 0 {
		t.Fatalf("post-DONE chunk should be dropped, got %v", trailing)
	}
}

func TestConvertOpenAIResponseToOpenAIPassthroughWithoutDone(t *testing.T) {
	var param any
	out := ConvertOpenAIResponseToOpenAI(context.Background(), "m", nil, nil, []byte(`{"id":"y"}`), &param)
	if len(out) != 1 || !bytes.Equal(out[0], []byte(`{"id":"y"}`)) {
		t.Fatalf("out = %v", out)
	}
}
