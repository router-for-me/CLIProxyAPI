package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamChunkerSplitsLongContent(t *testing.T) {
	chunker := streamChunker{chunkBytes: 16, firstChunkMax: 10}
	firstBudget := chunker.firstChunkMax
	line := []byte(`data: {"choices":[{"delta":{"content":"abcdefghijklmnopqrstuvwxyz"}}]}`)
	segments := chunker.splitLine(line, &firstBudget)
	if len(segments) < 2 {
		t.Fatalf("expected chunker to split into multiple segments, got %d", len(segments))
	}
	firstContent := mustDecodeContent(t, segments[0])
	if len(firstContent) > chunker.firstChunkMax {
		t.Fatalf("first chunk should respect firstChunkMax, got len=%d", len(firstContent))
	}
	last := mustDecodeContent(t, segments[len(segments)-1])
	if !strings.HasSuffix(last, "z") {
		t.Fatalf("last chunk should contain tail of text, got %q", last)
	}
}

func TestStreamChunkerKeepsDoneEvent(t *testing.T) {
	chunker := streamChunker{chunkBytes: 16, firstChunkMax: 10}
	firstBudget := chunker.firstChunkMax
	segments := chunker.splitLine([]byte("data: [DONE]"), &firstBudget)
	if len(segments) != 1 || string(segments[0]) != "data: [DONE]" {
		t.Fatalf("expected [DONE] to pass through untouched, got %#v", segments)
	}
}

func mustDecodeContent(t *testing.T, line []byte) string {
	t.Helper()
	raw := strings.TrimSpace(string(line))
	if !strings.HasPrefix(raw, "data: ") {
		t.Fatalf("line missing data prefix: %q", raw)
	}
	payload := raw[len("data: "):]
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		t.Fatalf("failed to decode chunk: %v", err)
	}
	if len(chunk.Choices) == 0 {
		t.Fatal("missing choices in chunk")
	}
	return chunk.Choices[0].Delta.Content
}
