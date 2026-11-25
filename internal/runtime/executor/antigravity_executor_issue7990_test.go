package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityIssue7990ExecuteStreamRejectsHTTP200WithoutSSEJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty body"},
		{name: "HTML body", body: "<html>502 Bad Gateway</html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			exec := NewAntigravityExecutor(newIssue7882PooledConfig("", true))
			auth := newIssue7882Auth(t.Name(), server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })
			markAntigravityCreditsPermanentlyDisabled(auth)
			ctx := cliproxyauth.WithAntigravityCredits(context.Background())
			result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-3.7-flash",
				Payload: []byte(`{"model":"gemini-3.7-flash","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatOpenAI,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
				if len(chunk.Payload) != 0 {
					t.Fatalf("ExecuteStream() emitted payload for invalid upstream response: %q", chunk.Payload)
				}
			}
			statusError, ok := streamErr.(interface {
				StatusCode() int
				ErrorCode() string
			})
			if !ok || statusError.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() stream error = %v (%T), want 502", streamErr, streamErr)
			}
			if statusError.ErrorCode() != cliproxyauth.ErrorCodeTransientTransport {
				t.Fatalf("ExecuteStream() error code = %q, want %q", statusError.ErrorCode(), cliproxyauth.ErrorCodeTransientTransport)
			}
			if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
				t.Fatal("invalid upstream response cleared prior credits failure state")
			}
		})
	}
}
