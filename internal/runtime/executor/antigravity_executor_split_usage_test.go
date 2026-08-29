package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// antigravitySplitTerminalSSE reproduces the only upstream shape that makes
// FilterSSEUsageMetadata forward real usageMetadata on a chunk without
// finishReason: a chunk carrying finishReason but no usage, whose traceId is
// then matched by a usage-only tail chunk. The tail chunk is terminal, so the
// stream must end with exactly one finish_reason that carries the usage.
const antigravitySplitTerminalSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"first"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.7-flash","responseId":"resp-split"},"traceId":"trace-split"}

data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"modelVersion":"gemini-3.7-flash","responseId":"resp-split"},"traceId":"trace-split"}

`

// TestAntigravityStreamFinalizesSplitTerminalUsageOnce covers a terminal
// finishReason and its usage arriving in separate chunks: the stream must carry
// exactly one finish_reason, on the last chunk, together with the token counts.
func TestAntigravityStreamFinalizesSplitTerminalUsageOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, antigravitySplitTerminalSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var (
		chunks      [][]byte
		finishIndex = -1
	)
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		if reason := gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String(); reason != "" {
			if finishIndex >= 0 {
				t.Fatalf("more than one terminal chunk: %s", chunk.Payload)
			}
			if reason != "stop" {
				t.Fatalf("finish_reason = %q, want stop", reason)
			}
			finishIndex = len(chunks)
		}
		chunks = append(chunks, chunk.Payload)
	}

	if finishIndex < 0 {
		t.Fatal("expected a terminal chunk")
	}
	if finishIndex != len(chunks)-1 {
		t.Fatalf("terminal chunk at index %d of %d chunks, want the last one", finishIndex, len(chunks))
	}
	terminal := chunks[finishIndex]
	if got := gjson.GetBytes(terminal, "usage.total_tokens").Int(); got != 33 {
		t.Fatalf("terminal total_tokens = %d, want 33", got)
	}
	if got := gjson.GetBytes(terminal, "usage.prompt_tokens").Int(); got != 11 {
		t.Fatalf("terminal prompt_tokens = %d, want 11", got)
	}
}
