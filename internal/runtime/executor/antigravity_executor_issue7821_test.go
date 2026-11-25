package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antigravityRateLimitNoRetryInfoBody = `{
	"error": {
		"code": 429,
		"message": "Too many requests.",
		"status": "RESOURCE_EXHAUSTED",
		"details": [
			{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo",
				"reason": "RATE_LIMIT_EXCEEDED",
				"domain": "cloudcode-pa.googleapis.com"
			}
		]
	}
}`

const antigravityRateLimitNoRetryInfoSSEFrame = `{"error":{"code":429,"message":"Too many requests.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`

func newIssue7821TestAuth(id, baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         id,
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"access_token":  "token-" + id,
			"refresh_token": "refresh-" + id,
			"project_id":    "project-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func TestAntigravityIssue7821_RetryAfterHeaderFallbackAcrossPaths(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)

	t.Run("Execute Gemini HTTP 429 RATE_LIMIT_EXCEEDED honors Retry-After header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-exec-gemini", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("Execute() err = %#v, want 429 statusErr", errExec)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("Execute() RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}
		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil {
			t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
		}
		if !inCooldown || remaining <= 0 || remaining > 45*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (true, (0, 45s])", inCooldown, remaining)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on Execute 429 with Retry-After: 45")
		}
	})

	t.Run("Execute Claude non-stream HTTP 429 RATE_LIMIT_EXCEEDED honors Retry-After header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-exec-claude", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-sonnet-4-6",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("Execute(claude) err = %#v, want 429 statusErr", errExec)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("Execute(claude) RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}
		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "claude-sonnet-4-6", time.Now())
		if errCooldown != nil {
			t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
		}
		if !inCooldown || remaining <= 0 || remaining > 45*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (true, (0, 45s])", inCooldown, remaining)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on Claude non-stream 429 with Retry-After: 45")
		}
	})

	t.Run("ExecuteStream HTTP 429 RATE_LIMIT_EXCEEDED honors Retry-After header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-stream", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errStream, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("ExecuteStream() err = %#v, want 429 statusErr", errStream)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("ExecuteStream() RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}
		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil {
			t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
		}
		if !inCooldown || remaining <= 0 || remaining > 45*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (true, (0, 45s])", inCooldown, remaining)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on ExecuteStream 429 with Retry-After: 45")
		}
	})

	t.Run("CountTokens HTTP 429 honors Retry-After header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Too many requests."}}`))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-count-tokens", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errTokens := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errTokens, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("CountTokens() err = %#v, want 429 statusErr", errTokens)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("CountTokens() RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on CountTokens 429")
		}
	})
}

func TestAntigravityIssue7821_RetryAfterThresholdsPrecedenceAndBoundaries(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)

	t.Run("generic 429 without RESOURCE_EXHAUSTED honors Retry-After on statusErr", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Rate limited by proxy"}}`))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-generic-429", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("Execute(generic 429) err = %#v, retryAfter = %v, want 45s", errExec, sErr.RetryAfter())
		}
		inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil {
			t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
		}
		if inCooldown {
			t.Fatal("generic non-RESOURCE_EXHAUSTED 429 must not record short cooldown")
		}
	})

	t.Run("empty-body 429 honors Retry-After header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-empty-body-429", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("Execute(empty body 429) err = %#v, retryAfter = %v, want 45s", errExec, sErr.RetryAfter())
		}
	})

	t.Run("future HTTP-date Retry-After triggers short cooldown and transport eviction", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", time.Now().Add(45*time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-http-date", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() < 35*time.Second || *sErr.RetryAfter() > 50*time.Second {
			t.Fatalf("Execute(HTTP-date) retryAfter = %v, want within [35s, 50s]", sErr.RetryAfter())
		}
		inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil || !inCooldown {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (true, nil)", inCooldown, errCooldown)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports for auth to be evicted on future HTTP-date Retry-After")
		}
	})

	t.Run("RATE_LIMIT_EXCEEDED with Retry-After below instant threshold skips short cooldown and transport eviction", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-instant-retry", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != time.Second {
			t.Fatalf("Execute(Retry-After: 1) retryAfter = %v, want 1s", sErr.RetryAfter())
		}
		inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil || inCooldown {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (false, nil)", inCooldown, errCooldown)
		}
		if !antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports to remain cached for instant retry (< 3s)")
		}
	})

	t.Run("RATE_LIMIT_EXCEEDED with Retry-After at or above 5m evicts transports without recording short cooldown", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-full-quota", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != 600*time.Second {
			t.Fatalf("Execute(Retry-After: 600) retryAfter = %v, want 600s", sErr.RetryAfter())
		}
		inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil || inCooldown {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (false, nil)", inCooldown, errCooldown)
		}
		if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports to be evicted on full quota exhaustion (>= 5m)")
		}
	})

	t.Run("JSON RetryInfo wins over Retry-After header", func(t *testing.T) {
		body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"12s"}]}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-body-wins", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != 12*time.Second {
			t.Fatalf("Execute(body wins) retryAfter = %v, want 12s", sErr.RetryAfter())
		}
		inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil || !inCooldown || remaining > 12*time.Second {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v, %v), want (true, <=12s, nil)", inCooldown, remaining, errCooldown)
		}
	})

	t.Run("JSON RetryInfo 0s preserves zero-delay semantics over Retry-After header", func(t *testing.T) {
		body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-body-zero-wins", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != 0 {
			t.Fatalf("Execute(body 0s wins) retryAfter = %v, want 0s", sErr.RetryAfter())
		}
		inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil || inCooldown {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (false, nil)", inCooldown, errCooldown)
		}
		if !antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports to remain cached for 0s body RetryInfo")
		}
	})

	t.Run("explicit Retry-After 0 preserves zero-delay instant retry semantics", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-zero-header", server.URL)
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.RetryAfter() == nil || *sErr.RetryAfter() != 0 {
			t.Fatalf("Execute(Retry-After: 0) retryAfter = %v, want 0s", sErr.RetryAfter())
		}
		inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
		if errCooldown != nil || inCooldown {
			t.Fatalf("antigravityIsInShortCooldownRequired() = (%v, %v), want (false, nil)", inCooldown, errCooldown)
		}
		if !antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
			t.Fatal("expected idle transports to remain cached for Retry-After: 0 instant retry")
		}
	})

	t.Run("negative, past HTTP-date, or invalid Retry-After header yields nil retryAfter", func(t *testing.T) {
		cases := []struct {
			name   string
			header string
		}{
			{name: "negative", header: "-5"},
			{name: "invalid", header: "soon"},
			{name: "past_date", header: time.Now().Add(-30 * time.Second).UTC().Format(http.TimeFormat)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", tc.header)
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
				}))
				defer server.Close()

				cfg := &config.Config{}
				exec := NewAntigravityExecutor(cfg)
				auth := newIssue7821TestAuth("issue7821-invalid-"+tc.name, server.URL)
				t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

				_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "gemini-3-flash",
					Payload: reqPayload,
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatAntigravity,
					ResponseFormat: sdktranslator.FormatAntigravity,
				})
				var sErr statusErr
				if !errors.As(errExec, &sErr) || sErr.RetryAfter() != nil {
					t.Fatalf("Execute(%s) retryAfter = %v, want nil", tc.name, sErr.RetryAfter())
				}
				inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, "gemini-3-flash", time.Now())
				if errCooldown != nil || inCooldown {
					t.Fatalf("antigravityIsInShortCooldownRequired(%s) = (%v, %v), want (false, nil)", tc.name, inCooldown, errCooldown)
				}
			})
		}
	})

	t.Run("mid-stream SSE 429 on HTTP 200 does not read initial HTTP 200 Retry-After header", func(t *testing.T) {
		for _, streaming := range []bool{false, true} {
			name := "claude_nonstream"
			model := "claude-sonnet-4-6"
			if streaming {
				name = "execute_stream"
				model = "gemini-3-flash"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Retry-After", "45")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("data: " + antigravityRateLimitNoRetryInfoSSEFrame + "\n\n"))
				}))
				defer server.Close()

				cfg := &config.Config{}
				exec := NewAntigravityExecutor(cfg)
				auth := newIssue7821TestAuth("issue7821-midstream-"+name, server.URL)
				t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

				var gotErr error
				if streaming {
					res, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
						Model:   model,
						Payload: reqPayload,
					}, cliproxyexecutor.Options{
						SourceFormat:   sdktranslator.FormatAntigravity,
						ResponseFormat: sdktranslator.FormatAntigravity,
					})
					if errStream != nil {
						gotErr = errStream
					} else if res != nil {
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								gotErr = chunk.Err
							}
						}
					}
				} else {
					_, gotErr = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:   model,
						Payload: reqPayload,
					}, cliproxyexecutor.Options{
						SourceFormat:   sdktranslator.FormatAntigravity,
						ResponseFormat: sdktranslator.FormatAntigravity,
					})
				}

				var sErr statusErr
				if !errors.As(gotErr, &sErr) || sErr.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("mid-stream %s err = %#v, want 429 statusErr", name, gotErr)
				}
				if sErr.RetryAfter() != nil {
					t.Fatalf("mid-stream %s RetryAfter() = %v, want nil", name, sErr.RetryAfter())
				}
				inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, model, time.Now())
				if errCooldown != nil || inCooldown {
					t.Fatalf("mid-stream %s short cooldown = (%v, %v), want (false, nil)", name, inCooldown, errCooldown)
				}
			})
		}
	})

	t.Run("HTTP 503 honors Retry-After header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"unavailable"}}`))
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewAntigravityExecutor(cfg)
		auth := newIssue7821TestAuth("issue7821-503", server.URL)
		auth.Status = cliproxyauth.StatusActive
		t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

		ctx := context.Background()
		mgr := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: "gemini-3-flash"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, errReg := mgr.Register(cliproxyauth.WithSkipPersist(ctx), auth); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3-flash",
			Payload: reqPayload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatAntigravity,
			ResponseFormat: sdktranslator.FormatAntigravity,
		})
		var sErr statusErr
		if !errors.As(errExec, &sErr) || sErr.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("Execute(503) err = %#v, want 503 statusErr", errExec)
		}
		if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 45*time.Second {
			t.Fatalf("Execute(503) RetryAfter() = %v, want 45s", sErr.RetryAfter())
		}

		beforeMark := time.Now()
		mgr.MarkResult(ctx, cliproxyauth.Result{
			AuthID:     auth.ID,
			Provider:   "antigravity",
			Model:      "gemini-3-flash",
			Success:    false,
			RetryAfter: sErr.RetryAfter(),
			Error: &cliproxyauth.Error{
				HTTPStatus: sErr.StatusCode(),
				Message:    sErr.Error(),
			},
		})
		got, ok := mgr.GetByID(auth.ID)
		if !ok || got == nil {
			t.Fatal("GetByID() missing credential")
		}
		state := got.ModelStates["gemini-3-flash"]
		if state == nil {
			t.Fatal("expected model state for gemini-3-flash")
		}
		remaining := state.NextRetryAfter.Sub(beforeMark)
		if remaining < 40*time.Second || remaining > 50*time.Second {
			t.Fatalf("MarkResult cooldown remaining = %v, want ~45s", remaining)
		}
	})
}

func TestAntigravityIssue7821_MarkResultHonorsRetryAfterHeaderWindow(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(antigravityRateLimitNoRetryInfoBody))
	}))
	defer server.Close()

	ctx := context.Background()
	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	cred := newIssue7821TestAuth("issue7821-mark-result", server.URL)
	cred.Status = cliproxyauth.StatusActive
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(cred) })

	mgr := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "antigravity", []*registry.ModelInfo{
		{ID: "gemini-3-flash"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
	})
	if _, errReg := mgr.Register(cliproxyauth.WithSkipPersist(ctx), cred); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	_, errExec := exec.Execute(ctx, cred, cliproxyexecutor.Request{
		Model:   "gemini-3-flash",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	var sErr statusErr
	if !errors.As(errExec, &sErr) {
		t.Fatalf("Execute() err = %#v, want statusErr", errExec)
	}

	beforeMark := time.Now()
	mgr.MarkResult(ctx, cliproxyauth.Result{
		AuthID:     cred.ID,
		Provider:   "antigravity",
		Model:      "gemini-3-flash",
		Success:    false,
		RetryAfter: sErr.RetryAfter(),
		Error: &cliproxyauth.Error{
			HTTPStatus: sErr.StatusCode(),
			Message:    sErr.Error(),
		},
	})

	got, ok := mgr.GetByID(cred.ID)
	if !ok || got == nil {
		t.Fatal("GetByID() missing credential")
	}
	state := got.ModelStates["gemini-3-flash"]
	if state == nil {
		t.Fatal("expected model state for gemini-3-flash")
	}
	remaining := state.NextRetryAfter.Sub(beforeMark)
	if remaining < 40*time.Second || remaining > 50*time.Second {
		t.Fatalf("MarkResult cooldown remaining = %v, want ~45s (not 1s generic backoff)", remaining)
	}
}

func TestAntigravityIssue7821_DecideAndStatusErrWithHeaders(t *testing.T) {
	rateLimitBody := []byte(antigravityRateLimitNoRetryInfoBody)

	t.Run("decideAntigravity429WithHeaders classifies RATE_LIMIT_EXCEEDED across threshold boundaries", func(t *testing.T) {
		tests := []struct {
			name      string
			headers   http.Header
			body      []byte
			wantKind  antigravity429DecisionKind
			wantDelay *time.Duration
		}{
			{
				name:      "0s explicit Retry-After classifies as instant retry",
				headers:   http.Header{"Retry-After": []string{"0"}},
				body:      rateLimitBody,
				wantKind:  antigravity429DecisionInstantRetrySameAuth,
				wantDelay: durationPtr(0),
			},
			{
				name:      "2s below instant threshold",
				headers:   http.Header{"Retry-After": []string{"2"}},
				body:      rateLimitBody,
				wantKind:  antigravity429DecisionInstantRetrySameAuth,
				wantDelay: durationPtr(2 * time.Second),
			},
			{
				name:      "3s at short cooldown lower bound",
				headers:   http.Header{"Retry-After": []string{"3"}},
				body:      rateLimitBody,
				wantKind:  antigravity429DecisionShortCooldownSwitchAuth,
				wantDelay: durationPtr(3 * time.Second),
			},
			{
				name:      "299s below full quota threshold",
				headers:   http.Header{"retry-after": []string{"299"}},
				body:      rateLimitBody,
				wantKind:  antigravity429DecisionShortCooldownSwitchAuth,
				wantDelay: durationPtr(299 * time.Second),
			},
			{
				name:      "300s at full quota threshold",
				headers:   http.Header{"Retry-After": []string{"300"}},
				body:      rateLimitBody,
				wantKind:  antigravity429DecisionFullQuotaExhausted,
				wantDelay: durationPtr(300 * time.Second),
			},
			{
				name:      "empty body still captures Retry-After while keeping soft retry",
				headers:   http.Header{"Retry-After": []string{"45"}},
				body:      nil,
				wantKind:  antigravity429DecisionSoftRetry,
				wantDelay: durationPtr(45 * time.Second),
			},
			{
				name:      "absent header keeps soft retry and nil delay",
				headers:   nil,
				body:      rateLimitBody,
				wantKind:  antigravity429DecisionSoftRetry,
				wantDelay: nil,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				decision := decideAntigravity429WithHeaders(tc.body, tc.headers)
				if decision.kind != tc.wantKind {
					t.Fatalf("decideAntigravity429WithHeaders().kind = %q, want %q", decision.kind, tc.wantKind)
				}
				if tc.wantDelay == nil {
					if decision.retryAfter != nil {
						t.Fatalf("decideAntigravity429WithHeaders().retryAfter = %v, want nil", *decision.retryAfter)
					}
					return
				}
				if decision.retryAfter == nil || *decision.retryAfter != *tc.wantDelay {
					t.Fatalf("decideAntigravity429WithHeaders().retryAfter = %v, want %v", decision.retryAfter, *tc.wantDelay)
				}
			})
		}
	})

	t.Run("newAntigravityStatusErrWithHeaders honors retry hints on 429 and 503", func(t *testing.T) {
		err429 := newAntigravityStatusErrWithHeaders(http.StatusTooManyRequests, http.Header{"retry-after": []string{"45"}}, rateLimitBody)
		if err429.RetryAfter() == nil || *err429.RetryAfter() != 45*time.Second {
			t.Fatalf("newAntigravityStatusErrWithHeaders(429).RetryAfter() = %v, want 45s", err429.RetryAfter())
		}

		capacityBody := []byte(`{"error":{"code":503,"status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"17s"}]}}`)
		for _, tc := range []struct {
			name    string
			headers http.Header
			body    []byte
			want    time.Duration
		}{
			{name: "Retry-After header", headers: http.Header{"Retry-After": []string{"45"}}, body: rateLimitBody, want: 45 * time.Second},
			{name: "RetryInfo body", body: capacityBody, want: 17 * time.Second},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err503 := newAntigravityStatusErrWithHeaders(http.StatusServiceUnavailable, tc.headers, tc.body)
				if err503.RetryAfter() == nil || *err503.RetryAfter() != tc.want {
					t.Fatalf("newAntigravityStatusErrWithHeaders(503).RetryAfter() = %v, want %v", err503.RetryAfter(), tc.want)
				}
			})
		}
	})
}

func durationPtr(d time.Duration) *time.Duration {
	return &d
}
