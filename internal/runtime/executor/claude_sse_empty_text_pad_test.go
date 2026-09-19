package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityExecutor_ClaudeStreamThinkingOnlyStopPadsEmptyText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hidden reasoning","thought":true}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-thinking-only"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":4,"totalTokenCount":14},"modelVersion":"gemini-3.7-flash","responseId":"resp-thinking-only"}}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	chunks := collectAntigravityStream(t, server.URL, sdktranslator.FormatClaude, sdktranslator.FormatClaude,
		`{"model":"gemini-3.7-flash","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	joined := string(bytes.Join(chunks, nil))
	assertClaudeEmptyTextPadBeforeMessageDelta(t, joined)
	if got := gjson.Get(sseDataForEvent(t, joined, "message_delta"), "delta.stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn:\n%s", got, joined)
	}
}

func TestAntigravityExecutor_ClaudeStreamThinkingOnlyMaxTokensDoesNotPad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hidden reasoning","thought":true}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-thinking-max"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":4,"totalTokenCount":14},"modelVersion":"gemini-3.7-flash","responseId":"resp-thinking-max"}}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	chunks := collectAntigravityStream(t, server.URL, sdktranslator.FormatClaude, sdktranslator.FormatClaude,
		`{"model":"gemini-3.7-flash","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	joined := string(bytes.Join(chunks, nil))
	if strings.Contains(joined, `"content_block":{"type":"text"`) {
		t.Fatalf("MAX_TOKENS thinking-only stop must not pad empty text:\n%s", joined)
	}
	if got := gjson.Get(sseDataForEvent(t, joined, "message_delta"), "delta.stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens:\n%s", got, joined)
	}
}

func TestAntigravityExecutor_ClaudeNonStreamThinkingOnlyStopPadsEmptyText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"text":"hidden reasoning","thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":4,"totalTokenCount":14},"modelVersion":"gemini-3.7-flash","responseId":"resp-thinking-only-ns"}}`)
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	resp, errExecute := executor.Execute(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"model":"gemini-3.7-flash","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	blocks := gjson.GetBytes(resp.Payload, "content").Array()
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want thinking + empty text: %s", len(blocks), resp.Payload)
	}
	if blocks[0].Get("type").String() != "thinking" || blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "" {
		t.Fatalf("expected thinking + empty text pad, got: %s", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn: %s", got, resp.Payload)
	}
}

func TestGeminiExecutor_ClaudeStreamThinkingOnlyStopPadsEmptyText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"hidden reasoning","thought":true}]}}],"modelVersion":"gemini-2.0-flash","responseId":"resp-thinking-only"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":4,"totalTokenCount":14},"modelVersion":"gemini-2.0-flash","responseId":"resp-thinking-only"}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	chunks := collectGeminiClaudeStream(t, server.URL)
	joined := string(bytes.Join(chunks, nil))
	assertClaudeEmptyTextPadBeforeMessageDelta(t, joined)
	if got := gjson.Get(sseDataForEvent(t, joined, "message_delta"), "delta.stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn:\n%s", got, joined)
	}
}

func TestGeminiExecutor_ClaudeStreamThinkingOnlyMaxTokensDoesNotPad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"hidden reasoning","thought":true}]}}],"modelVersion":"gemini-2.0-flash","responseId":"resp-thinking-max"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":4,"totalTokenCount":14},"modelVersion":"gemini-2.0-flash","responseId":"resp-thinking-max"}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	chunks := collectGeminiClaudeStream(t, server.URL)
	joined := string(bytes.Join(chunks, nil))
	if strings.Contains(joined, `"content_block":{"type":"text"`) {
		t.Fatalf("MAX_TOKENS thinking-only stop must not pad empty text:\n%s", joined)
	}
	if got := gjson.Get(sseDataForEvent(t, joined, "message_delta"), "delta.stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens:\n%s", got, joined)
	}
}

func TestGeminiExecutor_ClaudeNonStreamThinkingOnlyStopPadsEmptyText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hidden reasoning","thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":4,"totalTokenCount":14},"modelVersion":"gemini-2.0-flash","responseId":"resp-thinking-only-ns"}`)
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{RequestRetry: 1})
	resp, errExecute := executor.Execute(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-2.0-flash",
		Payload: []byte(`{"model":"gemini-2.0-flash","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	blocks := gjson.GetBytes(resp.Payload, "content").Array()
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want thinking + empty text: %s", len(blocks), resp.Payload)
	}
	if blocks[0].Get("type").String() != "thinking" || blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "" {
		t.Fatalf("expected thinking + empty text pad, got: %s", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn: %s", got, resp.Payload)
	}
}

func collectGeminiClaudeStream(t *testing.T, baseURL string) [][]byte {
	t.Helper()
	executor := NewGeminiExecutor(&config.Config{RequestRetry: 1})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "test-key", "base_url": baseURL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-2.0-flash",
		Payload: []byte(`{"model":"gemini-2.0-flash","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var chunks [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk.Payload)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	return chunks
}

func assertClaudeEmptyTextPadBeforeMessageDelta(t *testing.T, output string) {
	t.Helper()
	emptyTextAt := strings.Index(output, `"content_block":{"type":"text","text":""}`)
	deltaAt := strings.Index(output, `"type":"message_delta"`)
	if emptyTextAt < 0 {
		t.Fatalf("expected empty text pad, got:\n%s", output)
	}
	if deltaAt < 0 {
		t.Fatalf("expected message_delta, got:\n%s", output)
	}
	if emptyTextAt > deltaAt {
		t.Fatalf("empty text pad must appear before message_delta:\n%s", output)
	}
	if strings.Contains(output, `"stop_reason":"max_tokens"`) {
		t.Fatalf("must not forge max_tokens:\n%s", output)
	}
}

func sseDataForEvent(t *testing.T, sse, event string) string {
	t.Helper()
	marker := "event: " + event + "\n"
	idx := strings.Index(sse, marker)
	if idx < 0 {
		t.Fatalf("event %q not found in:\n%s", event, sse)
	}
	rest := sse[idx+len(marker):]
	if !strings.HasPrefix(rest, "data: ") {
		t.Fatalf("expected data line after %q in:\n%s", event, sse)
	}
	line := strings.TrimPrefix(rest, "data: ")
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	return line
}
