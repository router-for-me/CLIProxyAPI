package auth

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestStreamRewriter_RewriteChunk_SplitJSONDataLinePreservesPayload(t *testing.T) {
	t.Run("split_mid_object", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		part1 := []byte(`data: {"type":"response.created","response":{"model":"gpt-5.4"`)
		part2 := []byte(`,"text":"hello world"}}` + "\n\n")

		if got1 := rewriter.RewriteChunk(part1); got1 != nil {
			t.Fatalf("first partial chunk should buffer, got %q", string(got1))
		}
		got2 := string(rewriter.RewriteChunk(part2))
		tail := string(rewriter.Finish())
		combined := got2 + tail

		if !strings.Contains(combined, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("combined output = %q, want rewritten model gpt-5.4-fast", combined)
		}
		if strings.Contains(combined, `"model":"gpt-5.4"`) {
			t.Fatalf("combined output still contains upstream model: %q", combined)
		}
		if !strings.Contains(combined, `"text":"hello world"`) {
			t.Fatalf("combined output corrupted payload: %q", combined)
		}
		_, jsonData, ok := extractSSEDataLine([]byte(strings.Split(combined, "\n")[0]))
		if !ok || !ojson.ValidBytes(jsonData) {
			t.Fatalf("rewritten data line is not valid JSON: %q", combined)
		}
	})

	t.Run("split_inside_json_string", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		part1 := []byte(`data: {"model":"gpt-5.4","text":"hel`)
		part2 := []byte(`lo world"}` + "\n\n")

		if got1 := rewriter.RewriteChunk(part1); got1 != nil {
			t.Fatalf("first partial chunk should buffer, got %q", string(got1))
		}
		got2 := string(rewriter.RewriteChunk(part2))
		if !strings.Contains(got2, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("got = %q, want rewritten model", got2)
		}
		if !strings.Contains(got2, `"text":"hello world"`) {
			t.Fatalf("got = %q, want intact string without injected newline", got2)
		}
	})

	t.Run("split_raw_json", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		part1 := []byte(`{"model":"gpt-5.4","text":"hel`)
		part2 := []byte(`lo"}`)

		if got1 := rewriter.RewriteChunk(part1); got1 != nil {
			t.Fatalf("first partial raw JSON chunk should buffer, got %q", string(got1))
		}
		got2 := string(rewriter.RewriteChunk(part2))
		if !ojson.ValidBytes([]byte(got2)) {
			t.Fatalf("rewritten raw JSON is invalid: %q", got2)
		}
		if ojson.Get(got2, "model").String() != "gpt-5.4-fast" || ojson.Get(got2, "text").String() != "hello" {
			t.Fatalf("unexpected rewritten raw JSON: %q", got2)
		}
	})
}

func TestStreamRewriter_RewriteChunk_AfterCompletePreservedWithInvalidDataLine(t *testing.T) {
	t.Run("with_pending_event", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		chunk1 := []byte("event: custom\ndata: {invalid-json\n\n" + `data: {"model":"gpt-5.4"`)
		chunk2 := []byte(`,"text":"ok"}` + "\n\n")

		got1 := string(rewriter.RewriteChunk(chunk1))
		if !strings.Contains(got1, "event: custom\ndata: {invalid-json") {
			t.Fatalf("chunk1 should emit completed invalid frame unchanged, got %q", got1)
		}

		got2 := string(rewriter.RewriteChunk(chunk2))
		tail := string(rewriter.Finish())
		rest := got2 + tail

		if !strings.Contains(rest, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("buffered afterComplete lost or not rewritten: %q", rest)
		}
		if !strings.Contains(rest, `"text":"ok"`) {
			t.Fatalf("buffered afterComplete payload corrupted: %q", rest)
		}
		if strings.Contains(rest, "{invalid-json") {
			t.Fatalf("earlier invalid line leaked into subsequent chunk: %q", rest)
		}
	})

	t.Run("without_pending_event", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		chunk1 := []byte("data: {invalid-json\n\n" + `data: {"model":"gpt-5.4"`)
		chunk2 := []byte(`,"text":"ok"}` + "\n\n")

		got1 := string(rewriter.RewriteChunk(chunk1))
		if !strings.Contains(got1, "data: {invalid-json") {
			t.Fatalf("chunk1 should emit completed invalid line unchanged, got %q", got1)
		}

		got2 := string(rewriter.RewriteChunk(chunk2))
		tail := string(rewriter.Finish())
		rest := got2 + tail

		if !strings.Contains(rest, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("buffered afterComplete corrupted or not rewritten: %q", rest)
		}
		if !strings.Contains(rest, `"text":"ok"`) {
			t.Fatalf("buffered afterComplete payload corrupted: %q", rest)
		}
		if strings.Contains(rest, "{invalid-json") {
			t.Fatalf("earlier invalid line leaked into afterComplete: %q", rest)
		}
	})
}

func TestStreamRewriter_RewriteChunk_LargeCompleteChunkRewritten(t *testing.T) {
	largePadding := strings.Repeat("a", maxPendingBufSize+4096)

	t.Run("complete_sse_chunk_over_1mb", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		chunk := []byte("event: response.completed\n" + `data: {"type":"response.completed","response":{"model":"gpt-5.4","blob":"` + largePadding + `"}}` + "\n\n")

		got := string(rewriter.RewriteChunk(chunk))
		if !strings.Contains(got, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("large complete SSE chunk skipped model rewriting (len=%d)", len(got))
		}
		if strings.Contains(got, `"model":"gpt-5.4"`) {
			t.Fatalf("large complete SSE chunk leaked upstream model")
		}
		if len(rewriter.pendingBuf) != 0 {
			t.Fatalf("expected empty pendingBuf, got len=%d", len(rewriter.pendingBuf))
		}
	})

	t.Run("complete_raw_json_chunk_over_1mb", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		chunk := []byte(`{"model":"gpt-5.4","blob":"` + largePadding + `"}`)

		got := string(rewriter.RewriteChunk(chunk))
		if !strings.Contains(got, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("large complete raw JSON chunk skipped model rewriting (len=%d)", len(got))
		}
		if strings.Contains(got, `"model":"gpt-5.4"`) {
			t.Fatalf("large complete raw JSON chunk leaked upstream model")
		}
		if len(rewriter.pendingBuf) != 0 {
			t.Fatalf("expected empty pendingBuf, got len=%d", len(rewriter.pendingBuf))
		}
	})

	t.Run("oversized_incomplete_afterComplete_not_buffered", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		chunk := []byte(`data: {"model":"gpt-5.4"}` + "\n\n" + `data: {"partial":"` + largePadding)

		got := string(rewriter.RewriteChunk(chunk))
		if !strings.Contains(got, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("complete frame before oversized afterComplete was not rewritten")
		}
		if len(rewriter.pendingBuf) > maxPendingBufSize {
			t.Fatalf("pendingBuf exceeded cap: %d > %d", len(rewriter.pendingBuf), maxPendingBufSize)
		}
	})

	t.Run("oversized_incomplete_chunk_falls_back_to_line_rewrite", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		chunk := []byte(`data: {"model":"gpt-5.4"}` + "\n" + `data: {"partial":"` + largePadding)

		got := string(rewriter.RewriteChunk(chunk))
		if !strings.Contains(got, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("valid line inside oversized incomplete chunk was not rewritten")
		}
		if len(rewriter.pendingBuf) != 0 {
			t.Fatalf("oversized incomplete chunk should not be buffered, got len=%d", len(rewriter.pendingBuf))
		}
	})
}

func TestRewriteForceMappedStreamChunk_SplitJSONDataLineEmitsSingleRewrittenEvent(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
	part1 := []byte(`data: {"type":"response.created","response":{"model":"gpt-5.4"`)
	part2 := []byte(`,"text":"hello world"}}` + "\n\n")

	got1 := rewriteForceMappedStreamChunk(rewriter, part1)
	if len(got1) != 0 {
		t.Fatalf("first partial chunk should be buffered without early emission, got %q", string(got1))
	}

	got2 := string(rewriteForceMappedStreamChunk(rewriter, part2))
	tail := string(finishForceMappedStreamChunks(rewriter))
	combined := got2 + tail

	if count := strings.Count(combined, "data: "); count != 1 {
		t.Fatalf("data line count = %d, want 1; combined=%q", count, combined)
	}
	if !strings.Contains(combined, `"model":"gpt-5.4-fast"`) {
		t.Fatalf("combined output = %q, want rewritten model gpt-5.4-fast", combined)
	}
	if strings.Contains(combined, `"model":"gpt-5.4"`) {
		t.Fatalf("combined output leaked upstream model: %q", combined)
	}
	if !strings.Contains(combined, `"text":"hello world"`) {
		t.Fatalf("combined output corrupted payload: %q", combined)
	}
	_, jsonData, ok := extractSSEDataLine([]byte(strings.Split(combined, "\n")[0]))
	if !ok || !ojson.ValidBytes(jsonData) {
		t.Fatalf("emitted data line is not valid JSON: %q", combined)
	}
}

func TestStreamRewriter_RewriteChunk_NewlineStrippedSSEMetadataThenData(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
	}{
		{name: "event", prefix: "event: response.completed"},
		{name: "id", prefix: "id: 7"},
		{name: "retry", prefix: "retry: 3000"},
		{name: "comment", prefix: ": keepalive"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
			if got1 := rewriter.RewriteChunk([]byte(tc.prefix)); got1 != nil {
				t.Fatalf("%s line should buffer, got %q", tc.name, string(got1))
			}
			got2 := string(rewriter.RewriteChunk([]byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4"}}`)))
			if !strings.Contains(got2, tc.prefix+"\ndata: ") {
				t.Fatalf("expected %s and data separated by newline, got %q", tc.name, got2)
			}
			if !strings.Contains(got2, `"model":"gpt-5.4-fast"`) {
				t.Fatalf("expected rewritten model, got %q", got2)
			}
			if strings.Contains(got2, `"model":"gpt-5.4"`) {
				t.Fatalf("output leaked upstream model: %q", got2)
			}
			if len(rewriter.pendingBuf) != 0 {
				t.Fatalf("expected empty pendingBuf, got %q", string(rewriter.pendingBuf))
			}
		})
	}

	t.Run("id_then_event_then_data", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		if got1 := rewriter.RewriteChunk([]byte("id: 7")); got1 != nil {
			t.Fatalf("id line should buffer, got %q", string(got1))
		}
		if got2 := rewriter.RewriteChunk([]byte("event: response.completed")); got2 != nil {
			t.Fatalf("event line should buffer, got %q", string(got2))
		}
		got3 := string(rewriter.RewriteChunk([]byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4"}}`)))
		if !strings.Contains(got3, "id: 7\nevent: response.completed\ndata: ") {
			t.Fatalf("expected preserved SSE metadata lines, got %q", got3)
		}
		if !strings.Contains(got3, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("expected rewritten model, got %q", got3)
		}
	})

	t.Run("split_json_string_starting_with_data_prefix_not_split", func(t *testing.T) {
		rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
		part1 := []byte(`data: {"model":"gpt-5.4","text":"prefix `)
		part2 := []byte(`data: literal"}` + "\n\n")

		if got1 := rewriter.RewriteChunk(part1); got1 != nil {
			t.Fatalf("first partial chunk should buffer, got %q", string(got1))
		}
		got2 := string(rewriter.RewriteChunk(part2))
		if !strings.Contains(got2, `"model":"gpt-5.4-fast"`) {
			t.Fatalf("expected rewritten model, got %q", got2)
		}
		if !strings.Contains(got2, `"text":"prefix data: literal"`) {
			t.Fatalf("expected intact JSON string without newline before literal data:, got %q", got2)
		}
	})
}
