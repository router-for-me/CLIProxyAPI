package claude

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

const textChunk = `data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`

// The stream flag is resolved once per stream: later chunks must not depend on re-reading
// the (potentially very large) original request.
func TestConvertOpenAIResponseToClaude_StreamFlagResolvedOncePerStream(t *testing.T) {
	var param any
	first := ConvertOpenAIResponseToClaude(context.Background(), "test-model", []byte(`{"model":"m","stream":true}`), nil, []byte(textChunk), &param)
	if !bytes.Contains(bytes.Join(first, nil), []byte("content_block_delta")) {
		t.Fatalf("expected streaming events on first chunk, got %q", bytes.Join(first, nil))
	}

	// An empty original request would parse as non-streaming if it were re-read.
	second := ConvertOpenAIResponseToClaude(context.Background(), "test-model", nil, nil, []byte(textChunk), &param)
	if !bytes.Contains(bytes.Join(second, nil), []byte("content_block_delta")) {
		t.Fatalf("expected the cached stream flag to be reused, got %q", bytes.Join(second, nil))
	}
}

func TestConvertOpenAIResponseToClaude_NonStreamingRequestStaysNonStreaming(t *testing.T) {
	for _, original := range []string{`{"model":"m"}`, `{"model":"m","stream":false}`} {
		var param any
		out := ConvertOpenAIResponseToClaude(context.Background(), "test-model", []byte(original), nil, []byte(textChunk), &param)
		if bytes.Contains(bytes.Join(out, nil), []byte("content_block_delta")) {
			t.Fatalf("request %s must not produce streaming events, got %q", original, bytes.Join(out, nil))
		}
	}
}

// Claude Code style request: large tools array with "stream" as the last key.
func BenchmarkConvertOpenAIResponseToClaude_LargeOriginalRequest(b *testing.B) {
	var sb strings.Builder
	sb.WriteString(`{"model":"m","tools":[`)
	for i := 0; i < 60; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"name":"tool_%d","description":%q,"input_schema":{"type":"object","properties":{"a":{"type":"string"}}}}`, i, strings.Repeat("x", 5000))
	}
	sb.WriteString(`],"stream":true}`)
	original := []byte(sb.String())
	chunk := []byte(textChunk)

	var param any
	b.SetBytes(int64(len(original)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ConvertOpenAIResponseToClaude(context.Background(), "test-model", original, nil, chunk, &param)
	}
}
