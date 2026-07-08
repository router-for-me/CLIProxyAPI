package auth

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseWSDataEventTypesFromForwardedChunks(forwarded [][]byte) []string {
	var types []string
	for _, ch := range forwarded {
		ch = normalizeGluedSSEEvents(ch)
		for _, ln := range bytes.Split(ch, []byte("\n")) {
			ln = bytes.TrimSpace(ln)
			if !bytes.HasPrefix(ln, []byte("data:")) {
				continue
			}
			j := bytes.TrimSpace(ln[5:])
			if gjson.ValidBytes(j) {
				types = append(types, gjson.GetBytes(j, "type").String())
			}
		}
	}
	return types
}

func replayCodexForceMapLines(t *testing.T, lines [][]byte) []string {
	t.Helper()
	r := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
	var forwarded [][]byte
	for _, line := range lines {
		if out := rewriteForceMappedStreamChunk(r, line); len(out) > 0 {
			forwarded = append(forwarded, out)
		}
	}
	if tail := finishForceMappedStreamChunks(r); len(tail) > 0 {
		forwarded = append(forwarded, tail)
	}
	return parseWSDataEventTypesFromForwardedChunks(forwarded)
}

func TestCodexForceMapPerLineSSE_ForwardsCompleted(t *testing.T) {
	lines := [][]byte{
		[]byte("event: response.created"),
		[]byte(`data: {"type":"response.created","response":{"model":"gpt-5.4"}}`),
		[]byte("event: response.output_text.delta"),
		[]byte(`data: {"type":"response.output_text.delta","delta":"OK"}`),
		[]byte("event: response.completed"),
		[]byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4","output":[]}}`),
	}
	types := replayCodexForceMapLines(t, lines)
	found := false
	for _, typ := range types {
		if typ == "response.completed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing response.completed, types=%v", types)
	}
}

func TestRewriteForceMappedStreamChunk_FallbackWhenPendingBuffersEvent(t *testing.T) {
	r := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
	_ = rewriteForceMappedStreamChunk(r, []byte("event: response.completed"))
	out := rewriteForceMappedStreamChunk(r, []byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4","output":[]}}`))
	if len(out) == 0 {
		tail := finishForceMappedStreamChunks(r)
		if !bytes.Contains(tail, []byte("response.completed")) {
			t.Fatalf("expected completed in tail, got %q", tail)
		}
		return
	}
	if !strings.Contains(string(out), "response.completed") {
		t.Fatalf("out=%q", out)
	}
}
