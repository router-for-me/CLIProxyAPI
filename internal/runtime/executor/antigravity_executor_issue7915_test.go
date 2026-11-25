package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityIssue7915ClaudeNonStreamPreservesFilterErrorAcrossTerminalFrame(t *testing.T) {
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
			name:          "usage metadata only",
			terminalFrame: `{"response":{"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}}`,
		},
		{
			name:          "empty text carrier with finish and usage",
			terminalFrame: `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"BLOCKLIST"}],"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}}`,
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
			auth := newIssue7753AntigravityAuth("issue7915-filter-terminal-"+tc.name, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			resp, errExecute := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatClaude,
			})
			if errExecute == nil {
				t.Fatalf("executeClaudeNonStream() response = %s, error = nil; want HTTP 400 filter error", resp.Payload)
			}
			var status interface{ StatusCode() int }
			if !errors.As(errExecute, &status) {
				t.Fatalf("executeClaudeNonStream() error type = %T, want status error: %v", errExecute, errExecute)
			}
			if got := status.StatusCode(); got != http.StatusBadRequest {
				t.Fatalf("StatusCode() = %d, want %d: %v", got, http.StatusBadRequest, errExecute)
			}
			if !strings.Contains(errExecute.Error(), filterText) {
				t.Fatalf("error = %q, want original filter message %q", errExecute, filterText)
			}
		})
	}
}
