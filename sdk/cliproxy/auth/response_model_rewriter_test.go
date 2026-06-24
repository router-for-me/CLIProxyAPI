package auth

import (
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestStreamRewriter_RewriteChunk_KimiMessagesDataPrefixWithoutSpace(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "k2.5"})
	chunk := []byte("event:message_start\n" +
		`data:{"type":"message_start","message":{"model":"kimi-k2.5"}}` + "\n\n")

	got := string(rewriter.RewriteChunk(chunk))
	if !strings.Contains(got, `"model":"k2.5"`) {
		t.Fatalf("rewritten chunk = %q, want alias model k2.5", got)
	}
	if strings.Contains(got, "kimi-k2.5") {
		t.Fatalf("rewritten chunk still contains upstream model: %q", got)
	}
	if !strings.Contains(got, "data:{") {
		t.Fatalf("rewritten chunk should preserve data: prefix without space: %q", got)
	}
}

func TestStreamRewriter_RewriteChunk_AnthropicMessagesDataPrefixWithSpace(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "grok-latest"})
	chunk := []byte(`data: {"type":"message_start","message":{"model":"grok-4.3"}}` + "\n\n")

	got := string(rewriter.RewriteChunk(chunk))
	if !strings.Contains(got, `"model":"grok-latest"`) {
		t.Fatalf("rewritten chunk = %q, want alias model grok-latest", got)
	}
	if strings.Contains(got, "grok-4.3") {
		t.Fatalf("rewritten chunk still contains upstream model: %q", got)
	}
	if !strings.Contains(got, "data: {") {
		t.Fatalf("rewritten chunk should preserve spaced data: prefix: %q", got)
	}
}

func TestStreamRewriter_Finish_FlushesCodexResponsesEventChunk(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
	part1 := []byte("event: response.created\n")
	part2 := []byte(`data: {"type":"response.created","response":{"model":"gpt-5.4"}}` + "\n\n")

	got1 := rewriter.RewriteChunk(part1)
	if got1 != nil {
		t.Fatalf("first partial chunk should buffer, got %q", string(got1))
	}
	got2 := string(rewriter.RewriteChunk(part2))
	gotTail := string(rewriter.Finish())
	combined := got2 + gotTail
	if !strings.Contains(combined, "gpt-5.4-fast") {
		t.Fatalf("combined output = %q, want rewritten alias", combined)
	}
	if strings.Contains(combined, `"model":"gpt-5.4"`) {
		t.Fatalf("combined output still has upstream model: %q", combined)
	}
}

func TestStreamRewriter_RewriteChunk_CodexResponsesLineChunks(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
	lines := [][]byte{
		[]byte("event: response.created\n"),
		[]byte(`data: {"type":"response.created","response":{"model":"gpt-5.4"}}` + "\n"),
		[]byte("\n"),
		[]byte("event: response.completed\n"),
		[]byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4"}}` + "\n"),
		[]byte("\n"),
	}
	var out []byte
	for _, line := range lines {
		if rewritten := rewriter.RewriteChunk(line); len(rewritten) > 0 {
			out = append(out, rewritten...)
		}
	}
	if tail := rewriter.Finish(); len(tail) > 0 {
		out = append(out, tail...)
	}
	got := string(out)
	if !strings.Contains(got, "gpt-5.4-fast") {
		t.Fatalf("rewritten output = %q, want alias gpt-5.4-fast", got)
	}
	if strings.Contains(got, `"model":"gpt-5.4"`) {
		t.Fatalf("rewritten output still contains upstream model: %q", got)
	}
}

func TestRewriteForceMappedStreamChunk_CodexLineChunksDoNotDuplicateBufferedEvent(t *testing.T) {
	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "gpt-5.4-fast"})
	chunks := [][]byte{
		[]byte("event: response.created\n"),
		[]byte(`data: {"type":"response.created","response":{"model":"gpt-5.4"}}` + "\n\n"),
	}

	var out []byte
	for _, chunk := range chunks {
		if rewritten := rewriteForceMappedStreamChunk(rewriter, chunk); len(rewritten) > 0 {
			out = append(out, rewritten...)
		}
	}
	if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 {
		out = append(out, tail...)
	}

	got := string(out)
	if count := strings.Count(got, "event: response.created"); count != 1 {
		t.Fatalf("event count = %d, want 1; output=%q", count, got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("rewritten output = %q, want complete SSE frame terminator", got)
	}
	if !strings.Contains(got, `"model":"gpt-5.4-fast"`) {
		t.Fatalf("rewritten output = %q, want alias model", got)
	}
	if strings.Contains(got, `"model":"gpt-5.4"`) {
		t.Fatalf("rewritten output still contains upstream model: %q", got)
	}
}

func TestRewriteModelInResponse_AntigravityModelVersion(t *testing.T) {
	payload := []byte(`{"response":{"modelVersion":"gemini-3-flash","candidates":[{"content":{"role":"model","parts":[{"text":"AGYMSG"}]}}]}}`)
	got := string(rewriteModelInResponse(payload, "claude-haiku-4-5-20251001"))
	if !strings.Contains(got, `"modelVersion":"claude-haiku-4-5-20251001"`) {
		t.Fatalf("rewritten payload = %q, want alias modelVersion", got)
	}
	if strings.Contains(got, "gemini-3-flash") {
		t.Fatalf("rewritten payload still contains upstream modelVersion: %q", got)
	}
}

func TestStreamRewriter_RewriteChunk_LiveDerivedProviderChunks(t *testing.T) {
	cases := []struct {
		name         string
		rewriteModel string
		upstream     string
		chunk        string
	}{
		{
			name:         "kimi_chat_stream",
			rewriteModel: "k2.5",
			upstream:     "kimi-k2.5",
			chunk:        `data:{"id":"chatcmpl-live","object":"chat.completion.chunk","created":1782272323,"model":"kimi-k2.5","choices":[{"index":0,"delta":{"content":"KCHATS"},"finish_reason":null}]}` + "\n\n",
		},
		{
			name:         "kimi_messages_stream",
			rewriteModel: "k2.5",
			upstream:     "kimi-k2.5",
			chunk:        "event:message_start\n" + `data:{"type":"message_start","message":{"model":"kimi-k2.5"}}` + "\n\n",
		},
		{
			name:         "xai_messages_stream",
			rewriteModel: "grok-latest",
			upstream:     "grok-4.3",
			chunk:        `data: {"type":"message_start","message":{"model":"grok-4.3"}}` + "\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: tc.rewriteModel})
			got := string(rewriter.RewriteChunk([]byte(tc.chunk)))
			if !strings.Contains(got, tc.rewriteModel) {
				t.Fatalf("rewritten chunk = %q, want alias %q", got, tc.rewriteModel)
			}
			if strings.Contains(got, tc.upstream) {
				t.Fatalf("rewritten chunk still contains upstream %q: %q", tc.upstream, got)
			}
		})
	}
}
func TestRewriteSSEPayloadLines_CodexResponsesLiveFrame(t *testing.T) {
	chunk := []byte("event: response.created\n" +
		`data: {"type":"response.created","response":{"model":"gpt-5.4"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"model":"gpt-5.4"}}` + "\n\n")
	got := string(rewriteSSEPayloadLines(chunk, "gpt-5.4-fast"))
	if !strings.Contains(got, "gpt-5.4-fast") {
		t.Fatalf("rewritten chunk = %q, want alias gpt-5.4-fast", got)
	}
	if strings.Contains(got, `"model":"gpt-5.4"`) {
		t.Fatalf("rewritten chunk still contains upstream model: %q", got)
	}
}

func TestRewriteForceMappedResponse_NoRewriteWhenForceMappingDisabled(t *testing.T) {
	upstream := []byte(`{"model":"gpt-5.4","choices":[]}`)
	resp := &cliproxyexecutor.Response{Payload: append([]byte(nil), upstream...)}
	rewriteForceMappedResponse(resp, OAuthModelAliasResult{
		UpstreamModel: "gpt-5.4",
		ForceMapping:  false,
		OriginalAlias: "gpt-5.4-fast",
	})
	if string(resp.Payload) != string(upstream) {
		t.Fatalf("payload = %s, want unchanged %s", resp.Payload, upstream)
	}
}

func TestRewriteForceMappedStreamChunk_NoRewriteWhenRewriterNil(t *testing.T) {
	chunk := []byte(`data: {"model":"gpt-5.4"}` + "\n\n")
	got := rewriteForceMappedStreamChunk(nil, chunk)
	if string(got) != string(chunk) {
		t.Fatalf("chunk = %q, want unchanged upstream payload", got)
	}
}
