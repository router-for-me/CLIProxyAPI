package auth

import (
	"strings"
	"testing"
)

func TestStreamRewriter_RewriteChunk_SplitJSONDataLinePreservesPayload(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})

	part1 := []byte(`data: {"type":"message_start","message":{"model":"up`)
	part2 := []byte(`stream-model"}}` + "\n\n")

	var out []byte
	if got := rewriter.RewriteChunk(part1); len(got) > 0 {
		out = append(out, got...)
	}
	if got := rewriter.RewriteChunk(part2); len(got) > 0 {
		out = append(out, got...)
	}
	if tail := rewriter.Finish(); len(tail) > 0 {
		out = append(out, tail...)
	}

	got := string(out)
	if strings.Contains(got, "up\nstream-model") {
		t.Fatalf("split JSON data line was torn with newline: %q", got)
	}
	if !strings.Contains(got, `"model":"alias-model"`) {
		t.Fatalf("rewritten output = %q, want alias-model", got)
	}
	if strings.Contains(got, "upstream-model") {
		t.Fatalf("rewritten output still contains upstream-model: %q", got)
	}
}

func TestStreamRewriter_RewriteChunk_AfterCompletePreservedWithInvalidDataLine(t *testing.T) {
	cases := []struct {
		name   string
		chunk1 string
	}{
		{
			name:   "with_pending_event",
			chunk1: "event: malformed\ndata: {invalid-json\n\n" + `data: {"type":"message_start","message":{"model":"up`,
		},
		{
			name:   "without_pending_event",
			chunk1: "data: {invalid-json\n\n" + `data: {"type":"message_start","message":{"model":"up`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})
			chunk2 := `stream-model"}}` + "\n\n"

			var out []byte
			if got := rewriter.RewriteChunk([]byte(tc.chunk1)); len(got) > 0 {
				out = append(out, got...)
			}
			if got := rewriter.RewriteChunk([]byte(chunk2)); len(got) > 0 {
				out = append(out, got...)
			}
			if tail := rewriter.Finish(); len(tail) > 0 {
				out = append(out, tail...)
			}

			got := string(out)
			if !strings.Contains(got, "data: {invalid-json") {
				t.Fatalf("invalid data line was dropped: %q", got)
			}
			if !strings.Contains(got, `"model":"alias-model"`) {
				t.Fatalf("afterComplete was lost or corrupted; output = %q, want alias-model", got)
			}
			if strings.Contains(got, "upstream-model") {
				t.Fatalf("rewritten output still contains upstream-model: %q", got)
			}
		})
	}
}

func TestStreamRewriter_RewriteChunk_LargeCompleteChunkRewritten(t *testing.T) {
	padding := strings.Repeat("a", maxPendingBufSize+1024)

	cases := []struct {
		name  string
		chunk []byte
	}{
		{
			name:  "sse_over_1mb",
			chunk: []byte(`data: {"model":"upstream-model","padding":"` + padding + `"}` + "\n\n"),
		},
		{
			name:  "raw_json_over_1mb",
			chunk: []byte(`{"model":"upstream-model","padding":"` + padding + `"}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})
			got := string(rewriter.RewriteChunk(tc.chunk))
			if !strings.Contains(got, `"model":"alias-model"`) {
				t.Fatalf("large complete chunk was not rewritten; len=%d", len(got))
			}
			if strings.Contains(got, "upstream-model") {
				t.Fatalf("large complete chunk still contains upstream-model; len=%d", len(got))
			}
		})
	}
}
