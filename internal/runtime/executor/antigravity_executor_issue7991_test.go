package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityIssue7991ExecuteClaudeNonStreamRejectsHTTP200WithoutSSEJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		format sdktranslator.Format
		body   string
	}{
		{
			name:   "claude empty body",
			model:  "claude-sonnet-4-6",
			format: sdktranslator.FormatClaude,
			body:   "",
		},
		{
			name:   "claude HTML body",
			model:  "claude-sonnet-4-6",
			format: sdktranslator.FormatClaude,
			body:   "<html>502 Bad Gateway</html>",
		},
		{
			name:   "claude DONE sentinel only",
			model:  "claude-sonnet-4-6",
			format: sdktranslator.FormatClaude,
			body:   "data: [DONE]\n\n",
		},
		{
			name:   "openai empty body",
			model:  "gemini-3-pro",
			format: sdktranslator.FormatOpenAI,
			body:   "",
		},
		{
			name:   "openai HTML body",
			model:  "gemini-3-pro",
			format: sdktranslator.FormatOpenAI,
			body:   "<html>502 Bad Gateway</html>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			capturedPathCh := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case capturedPathCh <- r.URL.Path:
				default:
				}
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
			resp, errExecute := exec.Execute(ctx, auth, cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: issue7513RequestPayload(tc.format, tc.model, false),
			}, cliproxyexecutor.Options{
				SourceFormat:   tc.format,
				ResponseFormat: tc.format,
			})
			var capturedPath string
			select {
			case capturedPath = <-capturedPathCh:
			default:
			}
			if !strings.Contains(capturedPath, "streamGenerateContent") {
				t.Fatalf("request path = %q, want :streamGenerateContent via executeClaudeNonStream", capturedPath)
			}
			if errExecute == nil {
				t.Fatalf("Execute() returned HTTP 200 payload=%s with err=nil, want 502", resp.Payload)
			}
			if len(resp.Payload) != 0 {
				t.Fatalf("Execute() returned non-empty payload on error: %q", resp.Payload)
			}

			var statusError interface {
				StatusCode() int
				ErrorCode() string
			}
			if !errors.As(errExecute, &statusError) || statusError.StatusCode() != http.StatusBadGateway {
				t.Fatalf("Execute() error = %v (%T), want 502 status error", errExecute, errExecute)
			}
			if statusError.ErrorCode() != cliproxyauth.ErrorCodeTransientTransport {
				t.Fatalf("Execute() error code = %q, want %q", statusError.ErrorCode(), cliproxyauth.ErrorCodeTransientTransport)
			}
			if tc.format == sdktranslator.FormatClaude {
				errMsg := errExecute.Error()
				if !ojson.Valid(errMsg) || ojson.Get(errMsg, "type").String() != "error" || ojson.Get(errMsg, "error.type").String() != "api_error" || ojson.Get(errMsg, "error.message").String() == "" {
					t.Fatalf("Execute() Claude error body = %q, want valid Claude api_error JSON", errMsg)
				}
			}
			if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
				t.Fatal("invalid upstream response cleared prior credits failure state")
			}
		})
	}
}

func TestAntigravityIssue7991ExecuteClaudeNonStreamPreservesValidSSEWithoutFinishReason(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: "+`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello from sse"}]}}],"modelVersion":"claude-sonnet-4-6","responseId":"resp-7991"}}`+"\n\n")
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(newIssue7882PooledConfig("", true))
	auth := newIssue7882Auth(t.Name(), server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })
	markAntigravityCreditsPermanentlyDisabled(auth)

	ctx := cliproxyauth.WithAntigravityCredits(context.Background())
	resp, errExecute := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: issue7513RequestPayload(sdktranslator.FormatClaude, "claude-sonnet-4-6", false),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want nil", errExecute)
	}
	if got := ojson.GetBytes(resp.Payload, "content.0.text").String(); got != "hello from sse" {
		t.Fatalf("Execute() content.0.text = %q, want %q; payload=%s", got, "hello from sse", resp.Payload)
	}
	if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); loaded {
		t.Fatal("valid upstream response did not clear prior credits failure state")
	}
}
