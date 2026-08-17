package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// captureGeminiInteractionsUsagePlugin captures usage records emitted for the
// gemini-interactions provider so tests can assert failure classification.
type captureGeminiInteractionsUsagePlugin struct {
	records chan usage.Record
}

func (p *captureGeminiInteractionsUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.Provider != "gemini-interactions" {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

// TestGeminiInteractionsStreamHTTP200ErrorPublishesFailure guards the fix for
// upstream errors delivered inside a HTTP 200 SSE stream (Gemini Interactions /
// Responses API returns {"error": {code, message}} with status 200). Such
// errors must be recorded as a failed attempt (record.Failed == true) so the
// flusher routes them to usage_errors, and must surface as a StreamChunk error.
//
// Prior to the fix, emitFrame only parsed usage and forwarded frames; the
// error object was treated as a normal frame, no PublishFailure fired, and the
// aborted request disappeared from the Errors list entirely.
func TestGeminiInteractionsStreamHTTP200ErrorPublishesFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest server does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Upstream reports a 400-class API error in a stream that is still
		// HTTP 200 — the gRPC-gateway style of the Gemini Interactions API.
		_, _ = w.Write([]byte("event: error\ndata: {\"error\":{\"code\":400,\"message\":\"API Malformed and missing params\",\"status\":\"INVALID_ARGUMENT\"}}\n\n"))
		_, _ = w.Write([]byte("event: done\ndata: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	plugin := &captureGeminiInteractionsUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"model":"gemini-3.1-flash-lite","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, errExecute := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	sawErrChunk := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawErrChunk = true
		}
	}
	if !sawErrChunk {
		t.Fatal("stream did not surface an error chunk for the HTTP 200 error payload")
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("stream record Failed = false; want true for upstream error-in-200: %+v", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestStreamErrorFromPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantOK  bool
		wantMsg string
	}{
		{"empty", "", false, ""},
		{"not-json", "not json", false, ""},
		{"no-error-field", `{"event_type":"interaction.created"}`, false, ""},
		{"normal-chunk", `{"id":"x","choices":[{"delta":{"content":"hi"}}]}`, false, ""},
		{"gemini-error", `{"error":{"code":400,"message":"API Malformed and missing params","status":"INVALID_ARGUMENT"}}`, true, "API Malformed and missing params"},
		{"openai-error", `{"error":{"message":"bad","type":"invalid_request_error","code":400}}`, true, "bad"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err, ok := streamErrorFromPayload([]byte(c.payload))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && err.msg != c.wantMsg {
				t.Fatalf("msg = %q, want %q", err.msg, c.wantMsg)
			}
		})
	}
}
