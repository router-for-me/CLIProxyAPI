package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// antigravityStreamErrorFixture mirrors the upstream failure shapes reported in
// issue #6357: the backend answers HTTP 200 with an SSE stream whose payload
// carries a top-level error node instead of candidates.
func antigravityStreamErrorFixture(t *testing.T, name, errLine string, wantCode int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(errLine))
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
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("[%s] ExecuteStream() error = %v", name, errExecute)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if fr := gjson.GetBytes(chunk.Payload, "candidates.0.finishReason").String(); fr != "" {
			t.Fatalf("[%s] in-stream error payload must not synthesize a successful terminal chunk: %s", name, chunk.Payload)
		}
	}
	if streamErr == nil {
		t.Fatalf("[%s] expected the in-stream error payload to fail the stream, got a successful completion", name)
	}
	status, ok := streamErr.(statusErr)
	if !ok {
		t.Fatalf("[%s] stream error = %T, want statusErr", name, streamErr)
	}
	if status.code != wantCode {
		t.Fatalf("[%s] statusErr.code = %d, want %d", name, status.code, wantCode)
	}
}

// TestAntigravityStreamSurfacesInStreamErrorPayload locks the issue #6357 fix:
// an upstream error payload inside an HTTP 200 SSE stream must surface as a
// terminal stream error, not be silently dropped and finalized as a successful
// end_turn completion.
func TestAntigravityStreamSurfacesInStreamErrorPayload(t *testing.T) {
	t.Run("pretty 500", func(t *testing.T) {
		antigravityStreamErrorFixture(t, "pretty 500",
			"data: {\"error\": {\"code\": 500, \"message\": \"An internal error has occurred.\", \"status\": \"INTERNAL\"}}\n\n",
			http.StatusInternalServerError)
	})
	t.Run("minified 503", func(t *testing.T) {
		antigravityStreamErrorFixture(t, "minified 503",
			"data: {\"error\":{\"code\":503,\"message\":\"The service is currently unavailable.\",\"status\":\"UNAVAILABLE\"}}\n\n",
			http.StatusServiceUnavailable)
	})
	t.Run("minified 429", func(t *testing.T) {
		antigravityStreamErrorFixture(t, "minified 429",
			"data: {\"error\":{\"code\":429,\"message\":\"Resource has been exhausted.\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n",
			http.StatusTooManyRequests)
	})
}

// TestAntigravityStreamNormalCompletionStillSucceeds guards the happy path: a
// full upstream stream with candidates and an explicit finishReason must keep
// completing successfully with no terminal error.
func TestAntigravityStreamNormalCompletionStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi there\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"gemini-3.7-flash\",\"responseId\":\"resp-ok\",\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":3,\"totalTokenCount\":13}}}\n\n"))
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
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var lastPayload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("normal completion must not surface a stream error: %v", chunk.Err)
		}
		lastPayload = chunk.Payload
	}
	if fr := gjson.GetBytes(lastPayload, "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("terminal finishReason = %q, want STOP", fr)
	}
	if !strings.Contains(string(lastPayload), "hi there") {
		t.Fatalf("terminal chunk lost the candidate text: %s", lastPayload)
	}
}
