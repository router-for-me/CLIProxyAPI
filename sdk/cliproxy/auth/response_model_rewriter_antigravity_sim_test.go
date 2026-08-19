package auth

import (
	"context"
	"strings"
	"testing"

	gemresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	"github.com/tidwall/gjson"
)

func antigravityLiveSSEChunks(t *testing.T) [][]byte {
	t.Helper()
	rawOK := `{"response": {"candidates": [{"content": {"role": "model","parts": [{"text": "OK"}]}}],"usageMetadata": {"promptTokenCount": 21,"candidatesTokenCount": 1,"totalTokenCount": 131,"thoughtsTokenCount": 109},"modelVersion": "gemini-3-flash-a","responseId": "tjVCavaJBYjgz7IP-NnfSQ"},"traceId": "x","metadata": {}}`
	rawStop := `{"response": {"candidates": [{"content": {"role": "model","parts": [{"thoughtSignature": "sig","text": ""}]},"finishReason": "STOP"}],"usageMetadata": {"promptTokenCount": 21,"candidatesTokenCount": 1,"totalTokenCount": 131,"thoughtsTokenCount": 109},"modelVersion": "gemini-3-flash-a","responseId": "tjVCavaJBYjgz7IP-NnfSQ"},"traceId": "x","metadata": {}}`
	req := []byte(`{"model":"gemini-3.5-flash","input":[]}`)
	var param any
	var chunks [][]byte
	for _, raw := range []string{rawOK, rawStop} {
		chunks = append(chunks, gemresponses.ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.5-flash", req, req, []byte("data: "+raw), &param)...)
	}
	if len(chunks) == 0 {
		t.Fatal("translator produced no chunks")
	}
	return chunks
}

func TestAntigravityTranslatorEmitsCompletedWithoutRewriter(t *testing.T) {
	chunks := antigravityLiveSSEChunks(t)
	combined := string(joinBytes(chunks))
	if !strings.Contains(combined, "response.completed") {
		t.Fatalf("translator missing completed: chunks=%d preview=%q", len(chunks), trunc(combined, 400))
	}
}

func TestRewriteForceMappedStreamChunk_AntigravityTranslatorEventChunks_PreservesCompleted(t *testing.T) {
	chunks := antigravityLiveSSEChunks(t)
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gemini-3.5-flash"})
	var out []byte
	for _, ch := range chunks {
		if rewritten := rewriteForceMappedStreamChunk(rewriter, ch); len(rewritten) > 0 {
			out = append(out, rewritten...)
		}
	}
	if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 {
		out = append(out, tail...)
	}
	if !parseCompletedFromSSE(out) {
		t.Fatalf("rewriter output missing response.completed; preview=%q", trunc(string(out), 400))
	}
}

func TestRewriteForceMappedStreamChunk_AntigravityGluedEventFramesFlushCompleted(t *testing.T) {
	chunks := antigravityLiveSSEChunks(t)
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gemini-3.5-flash"})
	var out []byte
	for i, ch := range chunks {
		if rewritten := rewriteForceMappedStreamChunk(rewriter, ch); len(rewritten) > 0 {
			out = append(out, rewritten...)
		}
		if i == 1 && len(rewriter.pendingBuf) > 0 && strings.Contains(string(rewriter.pendingBuf), "}event:") {
			t.Log("confirmed glued frames: ...}event:...")
		}
	}
	if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 {
		out = append(out, tail...)
	}
	if !parseCompletedFromSSE(out) {
		t.Fatalf("expected completed after glued frames flush; preview=%q", trunc(string(out), 400))
	}
}

func joinBytes(parts [][]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func parseCompletedFromSSE(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if gjson.Get(line, "type").String() == "response.completed" {
			return true
		}
	}
	trim := strings.TrimSpace(string(payload))
	if strings.HasPrefix(trim, "{") && gjson.Get(trim, "type").String() == "response.completed" {
		return true
	}
	return false
}
