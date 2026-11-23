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

func TestAntigravityIssue7918ExecuteStreamPreservesFilterErrorAcrossTerminalFrame(t *testing.T) {
	const filterText = "This request was blocked by Gemini's filters. Please review https://policies.google.com/terms/generative-ai/use-policy"
	filterFrame := `{"candidates":[{"content":{"parts":[{"text":"` + filterText + `"}]}}]}`

	tests := []struct {
		name          string
		terminalFrame string
	}{
		{
			name:          "finish and usage metadata",
			terminalFrame: `{"response":{"candidates":[{"finishReason":"BLOCKLIST"}],"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}}`,
		},
		{
			name:          "empty inline data carrier",
			terminalFrame: `{"response":{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":""}}]},"finishReason":"BLOCKLIST"}],"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("data: " + filterFrame + "\n\n"))
				_, _ = w.Write([]byte("data: " + tc.terminalFrame + "\n\n"))
			}))
			defer server.Close()

			exec := NewAntigravityExecutor(&config.Config{})
			auth := newIssue7753AntigravityAuth("issue7918-filter-terminal-"+tc.name, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			streamed, streamErr := executeIssue7918AntigravityStream(t, exec, auth)
			if streamErr == nil {
				t.Fatalf("ExecuteStream() output = %q, error = nil; want HTTP 400 filter error", streamed)
			}
			if streamed != "" {
				t.Fatalf("ExecuteStream() emitted payload before filter error: %q", streamed)
			}
			var status interface{ StatusCode() int }
			if !errors.As(streamErr, &status) {
				t.Fatalf("ExecuteStream() error type = %T, want status error: %v", streamErr, streamErr)
			}
			if got := status.StatusCode(); got != http.StatusBadRequest {
				t.Fatalf("StatusCode() = %d, want %d: %v", got, http.StatusBadRequest, streamErr)
			}
			if !strings.Contains(streamErr.Error(), filterText) {
				t.Fatalf("error = %q, want original filter message %q", streamErr, filterText)
			}
		})
	}
}

func TestAntigravityIssue7918ExecuteStreamFlushesFilterBeforeRealCodeOutput(t *testing.T) {
	const filterText = "This request was blocked by Gemini's filters. Please review https://policies.google.com/terms/generative-ai/use-policy"
	filterFrame := `{"candidates":[{"content":{"parts":[{"text":"` + filterText + `"}]}}]}`
	contentFrame := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]},"finishReason":"STOP"}]}}`

	if !geminiPayloadHasContentParts([]byte(contentFrame)) {
		t.Fatal("content frame with executableCode was not recognized as real content")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + filterFrame + "\n\n"))
		_, _ = w.Write([]byte("data: " + contentFrame + "\n\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7753AntigravityAuth("issue7918-filter-real-code", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	streamed, streamErr := executeIssue7918AntigravityStream(t, exec, auth)
	if streamErr != nil {
		t.Fatalf("ExecuteStream() error = %v, want successful real content stream", streamErr)
	}
	if !strings.Contains(streamed, filterText) {
		t.Fatalf("ExecuteStream() output = %q, want flushed filter frame %q", streamed, filterText)
	}
	if !strings.Contains(streamed, "print(1)") {
		t.Fatalf("ExecuteStream() output = %q, want executableCode content", streamed)
	}
}

func executeIssue7918AntigravityStream(t *testing.T, exec *AntigravityExecutor, auth *cliproxyauth.Auth) (string, error) {
	t.Helper()
	result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var streamed strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		streamed.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	return streamed.String(), streamErr
}
