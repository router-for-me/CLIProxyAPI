package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func issue7992CountTokensRequest(format sdktranslator.Format) cliproxyexecutor.Request {
	switch format {
	case sdktranslator.FormatClaude:
		return cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`),
		}
	case sdktranslator.FormatAntigravity:
		return cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`),
		}
	default:
		return cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		}
	}
}

func TestAntigravityIssue7992CountTokensRejectsInvalidHTTP200Bodies(t *testing.T) {
	tests := []struct {
		name        string
		format      sdktranslator.Format
		contentType string
		body        string
	}{
		{
			name:        "gemini empty body",
			format:      sdktranslator.FormatGemini,
			contentType: "text/plain",
			body:        "",
		},
		{
			name:        "gemini HTML body",
			format:      sdktranslator.FormatGemini,
			contentType: "text/html",
			body:        "<html>Error</html>",
		},
		{
			name:        "claude HTML body",
			format:      sdktranslator.FormatClaude,
			contentType: "text/html",
			body:        "<html>Error</html>",
		},
		{
			name:        "antigravity passthrough HTML body",
			format:      sdktranslator.FormatAntigravity,
			contentType: "text/html",
			body:        "<html>Error</html>",
		},
		{
			name:        "gemini malformed JSON",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":42`,
		},
		{
			name:        "gemini non-object JSON array",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `[]`,
		},
		{
			name:        "gemini missing totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{}`,
		},
		{
			name:        "claude missing totalTokens",
			format:      sdktranslator.FormatClaude,
			contentType: "application/json",
			body:        `{}`,
		},
		{
			name:        "gemini null totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":null}`,
		},
		{
			name:        "gemini string totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":"42"}`,
		},
		{
			name:        "gemini negative totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":-1}`,
		},
		{
			name:        "gemini negative fractional totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":-0.5}`,
		},
		{
			name:        "gemini positive fractional totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":0.5}`,
		},
		{
			name:        "gemini int64 overflow totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":9223372036854775808}`,
		},
		{
			name:        "gemini uint64 wraparound totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"totalTokens":18446744073709551616}`,
		},
		{
			name:        "gemini null error without totalTokens",
			format:      sdktranslator.FormatGemini,
			contentType: "application/json",
			body:        `{"error":null}`,
		},
		{
			name:        "gemini HTML with error snippet",
			format:      sdktranslator.FormatGemini,
			contentType: "text/html",
			body:        `<html>{"error":{"code":403,"status":"PERMISSION_DENIED"}}</html>`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			capturedPathCh := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				select {
				case capturedPathCh <- r.URL.Path:
				default:
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			exec := NewAntigravityExecutor(newIssue7882PooledConfig("", false))
			auth := newIssue7882Auth(t.Name(), server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			resp, errCount := exec.CountTokens(context.Background(), auth, issue7992CountTokensRequest(tc.format), cliproxyexecutor.Options{
				SourceFormat:   tc.format,
				ResponseFormat: tc.format,
			})
			var capturedPath string
			select {
			case capturedPath = <-capturedPathCh:
			default:
			}
			if capturedPath != antigravityCountTokensPath {
				t.Fatalf("request path = %q, want %q", capturedPath, antigravityCountTokensPath)
			}
			if errCount == nil {
				t.Fatalf("CountTokens() returned HTTP 200 payload=%s with err=nil, want 502", string(resp.Payload))
			}
			if len(resp.Payload) != 0 {
				t.Fatalf("CountTokens() returned non-empty payload on error: %q", string(resp.Payload))
			}
			var sErr statusErr
			if !errors.As(errCount, &sErr) || sErr.StatusCode() != http.StatusBadGateway {
				t.Fatalf("CountTokens() err = %#v, want 502 statusErr", errCount)
			}
		})
	}
}

func TestAntigravityIssue7992CountTokensPreservesValidZeroAndPositiveTotals(t *testing.T) {
	tests := []struct {
		name      string
		format    sdktranslator.Format
		body      string
		wantField string
		wantCount int64
	}{
		{
			name:      "gemini explicit zero",
			format:    sdktranslator.FormatGemini,
			body:      `{"totalTokens":0}`,
			wantField: "totalTokens",
			wantCount: 0,
		},
		{
			name:      "claude explicit zero",
			format:    sdktranslator.FormatClaude,
			body:      `{"totalTokens":0}`,
			wantField: "input_tokens",
			wantCount: 0,
		},
		{
			name:      "gemini positive total",
			format:    sdktranslator.FormatGemini,
			body:      `{"totalTokens":42}`,
			wantField: "totalTokens",
			wantCount: 42,
		},
		{
			name:      "gemini null error with valid total",
			format:    sdktranslator.FormatGemini,
			body:      `{"error":null,"totalTokens":17}`,
			wantField: "totalTokens",
			wantCount: 17,
		},
		{
			name:      "gemini whitespace padded valid total",
			format:    sdktranslator.FormatGemini,
			body:      " \n {\"totalTokens\":42} \n ",
			wantField: "totalTokens",
			wantCount: 42,
		},
		{
			name:      "antigravity passthrough valid total",
			format:    sdktranslator.FormatAntigravity,
			body:      `{"totalTokens":9}`,
			wantField: "totalTokens",
			wantCount: 9,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			exec := NewAntigravityExecutor(newIssue7882PooledConfig("", false))
			auth := newIssue7882Auth(t.Name(), server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			resp, errCount := exec.CountTokens(context.Background(), auth, issue7992CountTokensRequest(tc.format), cliproxyexecutor.Options{
				SourceFormat:   tc.format,
				ResponseFormat: tc.format,
			})
			if errCount != nil {
				t.Fatalf("CountTokens() error = %v, want nil", errCount)
			}
			got := ojson.GetBytesNoCopy(resp.Payload, tc.wantField)
			if !got.Exists() || got.Type != ojson.Number || got.Int() != tc.wantCount {
				t.Fatalf("CountTokens() %s = %q (type=%v), want %d; payload=%s", tc.wantField, got.Raw, got.Type, tc.wantCount, string(resp.Payload))
			}
		})
	}
}
