package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorStreamHTTP200RateLimitErrorCarriesRawFrameAndClassification(t *testing.T) {
	const firstFrame = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"model\":\"claude-test\"}}\n\n"
	const errorFrame = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"subscription window exhausted\"}}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		_, _ = w.Write([]byte(firstFrame + errorFrame))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-test",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-test","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-test",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want message_start plus terminal error", len(chunks))
	}
	if got := string(chunks[0].Payload); got != firstFrame || chunks[0].Err != nil {
		t.Fatalf("first chunk changed: payload=%q err=%v", got, chunks[0].Err)
	}
	if got := string(chunks[1].Payload); got != errorFrame {
		t.Fatalf("terminal error frame changed: got %q want %q", got, errorFrame)
	}
	if chunks[1].Err == nil {
		t.Fatal("terminal error frame was not classified")
	}
	var status interface{ StatusCode() int }
	if !errors.As(chunks[1].Err, &status) || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("terminal status = %v, want 429", chunks[1].Err)
	}
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(chunks[1].Err, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("terminal error = %T %v, want credential-scoped", chunks[1].Err, chunks[1].Err)
	}
	var payloadBacked interface{ StreamErrorPayloadEncoded() bool }
	if !errors.As(chunks[1].Err, &payloadBacked) || !payloadBacked.StreamErrorPayloadEncoded() {
		t.Fatalf("terminal error = %T, want payload-backed marker", chunks[1].Err)
	}
	if strings.Contains(string(chunks[0].Payload), "event: error") {
		t.Fatal("terminal frame was merged into the preceding event")
	}
}

func TestClaudeStreamingEventErrorIgnoresOrdinaryClaudeEvent(t *testing.T) {
	event := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
	if err := claudeStreamingEventError(event, nil); err != nil {
		t.Fatalf("ordinary event classified as error: %v", err)
	}
}
