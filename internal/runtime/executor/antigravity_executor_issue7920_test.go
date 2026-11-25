package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityIssue7920_CountTokensSurfacesHTTP200InBandError(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(antigravityTOSBody))
	}))
	defer server.Close()

	cfg := newIssue7882PooledConfig("6h", false)
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7882Auth("issue7920-count-tokens-200-tos", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	_, errCount := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})

	var sErr statusErr
	if !errors.As(errCount, &sErr) {
		t.Fatalf("CountTokens() err = %#v, want statusErr", errCount)
	}
	if sErr.StatusCode() != http.StatusForbidden {
		t.Fatalf("StatusCode() = %d, want %d", sErr.StatusCode(), http.StatusForbidden)
	}
	if !sErr.IsTerminalTOSViolation() || !sErr.IsCredentialScoped() {
		t.Fatalf("TOS statusErr flags = (tos=%v, credScoped=%v), want (true, true)", sErr.IsTerminalTOSViolation(), sErr.IsCredentialScoped())
	}
	if !antigravityInTOSPause(cfg, auth) {
		t.Fatal("expected pool-wide TOS pause after HTTP 200 in-band error")
	}
	if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
		t.Fatal("expected idle transports for auth to be evicted after HTTP 200 in-band error")
	}
}

func TestAntigravityIssue7920_CountTokensAppliesHTTP200InBandErrorBookkeeping(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		statusCode     int
		credentialOnly bool
		useRetryAfter  bool
	}{
		{
			name:           "validation required",
			body:           antigravityValidationRequiredBody,
			statusCode:     http.StatusForbidden,
			credentialOnly: true,
		},
		{
			name:          "rate limit",
			body:          antigravityRateLimitNoRetryInfoBody,
			statusCode:    http.StatusTooManyRequests,
			useRetryAfter: true,
		},
		{
			name:           "unauthenticated",
			body:           `{"error":{"code":401,"message":"invalid credentials","status":"UNAUTHENTICATED"}}`,
			statusCode:     http.StatusUnauthorized,
			credentialOnly: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if tc.useRetryAfter {
					w.Header().Set("Retry-After", "45")
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			cfg := newIssue7882PooledConfig("", false)
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7882Auth("issue7920-count-tokens-"+tc.name, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			_, errCount := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gemini-3-flash",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			})

			var sErr statusErr
			if !errors.As(errCount, &sErr) || sErr.StatusCode() != tc.statusCode {
				t.Fatalf("CountTokens() err = %#v, want statusErr %d", errCount, tc.statusCode)
			}
			if tc.credentialOnly && !sErr.IsCredentialScoped() {
				t.Fatal("expected credential-scoped statusErr")
			}
			if tc.useRetryAfter && (sErr.RetryAfter() == nil || *sErr.RetryAfter() <= 0) {
				t.Fatalf("RetryAfter() = %v, want positive duration", sErr.RetryAfter())
			}
			if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
				t.Fatal("expected idle transports for auth to be evicted")
			}
		})
	}
}
