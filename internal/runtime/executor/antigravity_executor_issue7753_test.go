package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newIssue7753AntigravityAuth(id, baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         id,
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func issue7753TransportKey(cfg *config.Config, auth *cliproxyauth.Auth) antigravityTransportKey {
	settings := resolveAntigravityPoolSettings(cfg)
	return antigravityTransportKey{
		credential:          antigravityTransportScope(auth),
		base:                antigravityBaseTransport,
		shortMode:           settings.shortMode,
		idleConnTimeout:     settings.idleConnTimeout,
		maxIdleConnsPerHost: settings.maxIdleConnsPerHost,
	}
}

func TestAntigravityIssue7753_ExecuteStreamPendingJSONRecoversAfterMalformedBlankBoundary(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"bad\"\n\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"recovered\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-stream-blank", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	result, errExec := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	var streamed strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if !strings.Contains(streamed.String(), "recovered") {
		t.Fatalf("stream output = %q, want payload containing %q", streamed.String(), "recovered")
	}
}

func TestAntigravityIssue7753_ExecuteStreamPendingJSONIgnoresNonDataLinesDuringMultiline(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\n"))
		_, _ = w.Write([]byte(": keepalive\n"))
		_, _ = w.Write([]byte("   \n"))
		_, _ = w.Write([]byte("event: message\n"))
		_, _ = w.Write([]byte("id: 42\n"))
		_, _ = w.Write([]byte("retry: 5000\n"))
		_, _ = w.Write([]byte("data: \"parts\":[{\"text\":\"multiline-ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-stream-nondata", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	result, errExec := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	var streamed strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if !strings.Contains(streamed.String(), "multiline-ok") {
		t.Fatalf("stream output = %q, want payload containing %q", streamed.String(), "multiline-ok")
	}
}

func TestAntigravityIssue7753_ClaudeNonStreamPendingJSONRecoversAfterMalformedAndNonDataLines(t *testing.T) {
	tests := []struct {
		name     string
		sseBody  string
		wantText string
	}{
		{
			name: "truncated frame followed by blank boundary and valid frame",
			sseBody: "data: {\"bad\"\n\n" +
				"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"recovered\"}]},\"finishReason\":\"STOP\"}]}}\n\n",
			wantText: "recovered",
		},
		{
			name: "multiline data interleaved with sse comment and control lines",
			sseBody: "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\n" +
				": keepalive\n" +
				"   \n" +
				"event: message\n" +
				"id: 7\n" +
				"retry: 1000\n" +
				"data: \"parts\":[{\"text\":\"multiline-nonstream\"}]},\"finishReason\":\"STOP\"}]}}\n\n",
			wantText: "multiline-nonstream",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.sseBody))
			}))
			defer server.Close()

			cfg := &config.Config{}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-nonstream-"+tc.wantText, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			resp, errExec := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			})
			if errExec != nil {
				t.Fatalf("executeClaudeNonStream() error = %v", errExec)
			}
			if !strings.Contains(string(resp.Payload), tc.wantText) {
				t.Fatalf("response payload = %q, want substring %q", string(resp.Payload), tc.wantText)
			}
		})
	}
}

func TestAntigravityIssue7753_ClaudeNonStreamMidStream429CreditsExhausted(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-nonstream-429-credits", server.URL)
	t.Cleanup(func() {
		closeAntigravityAuthIdleTransports(auth)
		cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
	})

	ctx := cliproxyauth.WithAntigravityCredits(context.Background())
	_, errExec := exec.executeClaudeNonStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec == nil {
		t.Fatal("executeClaudeNonStream() = nil, want 429 error")
	}
	sErr, ok := errExec.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", errExec)
	}
	if sErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d (err = %v)", sErr.StatusCode(), http.StatusTooManyRequests, errExec)
	}
	rawState, loaded := antigravityCreditsFailureByAuth.Load(auth.ID)
	if !loaded {
		t.Fatal("expected auth to be marked permanently disabled in antigravityCreditsFailureByAuth")
	}
	state, okState := rawState.(antigravityCreditsFailureState)
	if !okState || !state.PermanentlyDisabled || !state.ExplicitBalanceExhausted {
		t.Fatalf("credits failure state = %+v, want PermanentlyDisabled=true ExplicitBalanceExhausted=true", state)
	}
	hint, okHint := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
	if !okHint || !hint.Known || hint.Available {
		t.Fatalf("credits hint = %+v (ok=%v), want Known=true Available=false", hint, okHint)
	}
	if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
		t.Fatal("expected idle transports for auth to be evicted on mid-stream 429")
	}
}

func TestAntigravityIssue7753_ClaudeNonStreamMidStream429ShortCooldownAndRetryAfter(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-nonstream-429-short", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	model := "claude-sonnet-4-6"
	_, errExec := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec == nil {
		t.Fatal("executeClaudeNonStream() = nil, want 429 error")
	}
	sErr, ok := errExec.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", errExec)
	}
	if sErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", sErr.StatusCode(), http.StatusTooManyRequests)
	}
	if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 30*time.Second {
		t.Fatalf("RetryAfter() = %v, want 30s", sErr.RetryAfter())
	}
	inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, model, time.Now())
	if errCooldown != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
	}
	if !inCooldown || remaining <= 0 {
		t.Fatalf("short cooldown = (%v, %s), want active short cooldown", inCooldown, remaining)
	}
	if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
		t.Fatal("expected idle transports for auth to be evicted on mid-stream 429 short cooldown")
	}
}

func TestAntigravityIssue7753_ClaudeNonStreamMidStreamNon403ErrorsSurfaced(t *testing.T) {
	tests := []struct {
		name     string
		sseBody  string
		wantCode int
	}{
		{
			name:     "mid-stream 503",
			sseBody:  "data: {\"error\":{\"code\":503,\"message\":\"overloaded\",\"status\":\"UNAVAILABLE\"}}\n\n",
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "mid-stream 500",
			sseBody:  "data: {\"error\":{\"code\":500,\"message\":\"internal\",\"status\":\"INTERNAL\"}}\n\n",
			wantCode: http.StatusInternalServerError,
		},
		{
			name:     "mid-stream 400",
			sseBody:  "data: {\"error\":{\"code\":400,\"message\":\"bad request\",\"status\":\"INVALID_ARGUMENT\"}}\n\n",
			wantCode: http.StatusBadRequest,
		},
		{
			name: "partial content followed by mid-stream 500",
			sseBody: "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}}\n\n" +
				"data: {\"error\":{\"code\":500,\"message\":\"stream failed\",\"status\":\"INTERNAL\"}}\n\n",
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.sseBody))
			}))
			defer server.Close()

			cfg := &config.Config{}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-nonstream-err-"+tc.name, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			_, errExec := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			})
			if errExec == nil {
				t.Fatalf("executeClaudeNonStream() = nil, want status %d", tc.wantCode)
			}
			sErr, ok := errExec.(statusErr)
			if !ok {
				t.Fatalf("error type = %T, want statusErr", errExec)
			}
			if sErr.StatusCode() != tc.wantCode {
				t.Fatalf("StatusCode() = %d, want %d (err = %v)", sErr.StatusCode(), tc.wantCode, errExec)
			}
		})
	}
}

func TestAntigravityIssue7753_ExecuteStreamMidStream429ShortCooldownBookkeeping(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-stream-429-short", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	model := "claude-sonnet-4-6"
	result, errExec := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() setup error = %v", errExec)
	}

	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
		}
	}
	if chunkErr == nil {
		t.Fatal("expected stream chunk error, got nil")
	}
	sErr, ok := chunkErr.(statusErr)
	if !ok {
		t.Fatalf("chunk error type = %T, want statusErr", chunkErr)
	}
	if sErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want %d", sErr.StatusCode(), http.StatusTooManyRequests)
	}
	if sErr.RetryAfter() == nil || *sErr.RetryAfter() != 30*time.Second {
		t.Fatalf("RetryAfter() = %v, want 30s", sErr.RetryAfter())
	}
	inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, model, time.Now())
	if errCooldown != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
	}
	if !inCooldown || remaining <= 0 {
		t.Fatalf("short cooldown = (%v, %s), want active short cooldown", inCooldown, remaining)
	}
	if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
		t.Fatal("expected idle transports for auth to be evicted on mid-stream 429 short cooldown")
	}
}

func TestAntigravityIssue7753_ExecuteStreamMidStream429CreditsExhaustedBookkeeping(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-stream-429-credits", server.URL)
	t.Cleanup(func() {
		closeAntigravityAuthIdleTransports(auth)
		cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
	})

	ctx := cliproxyauth.WithAntigravityCredits(context.Background())
	result, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() setup error = %v", errExec)
	}

	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
		}
	}
	if chunkErr == nil {
		t.Fatal("expected stream chunk error, got nil")
	}
	sErr, ok := chunkErr.(statusErr)
	if !ok || sErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("chunkErr = %v (%T), want 429 statusErr", chunkErr, chunkErr)
	}
	rawState, loaded := antigravityCreditsFailureByAuth.Load(auth.ID)
	if !loaded {
		t.Fatal("expected auth to be marked permanently disabled in antigravityCreditsFailureByAuth")
	}
	state, okState := rawState.(antigravityCreditsFailureState)
	if !okState || !state.PermanentlyDisabled || !state.ExplicitBalanceExhausted {
		t.Fatalf("credits failure state = %+v, want PermanentlyDisabled=true ExplicitBalanceExhausted=true", state)
	}
	hint, okHint := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
	if !okHint || !hint.Known || hint.Available {
		t.Fatalf("credits hint = %+v (ok=%v), want Known=true Available=false", hint, okHint)
	}
	if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
		t.Fatal("expected idle transports for auth to be evicted on mid-stream 429 credits exhaustion")
	}
}

func TestAntigravityIssue7753_ExecuteStreamMidStream429HomeKVError(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	kvClient := newFakeAntigravityKVClient()
	kvClient.setErr = errors.New("kv write failed")
	useFakeAntigravityKVClient(t, kvClient, true, nil)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-stream-429-kv-err", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	result, errExec := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() setup error = %v", errExec)
	}

	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
		}
	}
	if chunkErr == nil {
		t.Fatal("expected stream chunk error, got nil")
	}
	sErr, ok := chunkErr.(statusErr)
	if !ok || sErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("chunkErr = %v (%T), want 503 homeKVUnavailableStatusErr", chunkErr, chunkErr)
	}
}

func TestAntigravityIssue7753_ExecuteClaudeFormatMidStream429NotDegradedTo502(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
	}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-exec-claude-429", server.URL)
	t.Cleanup(func() {
		closeAntigravityAuthIdleTransports(auth)
		cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
	})

	ctx := cliproxyauth.WithAntigravityCredits(context.Background())
	_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExec == nil {
		t.Fatal("Execute() = nil, want 429 error")
	}
	sErr, ok := errExec.(statusErr)
	if !ok || sErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("err = %v (%T), want 429 statusErr (not 502 zero-output)", errExec, errExec)
	}
	if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
		t.Fatal("expected auth credits failure state to be recorded")
	}
}

func TestAntigravityIssue7753_ClaudeNonStreamMidStream429HomeKVError(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	kvClient := newFakeAntigravityKVClient()
	kvClient.setErr = errors.New("kv write failed")
	useFakeAntigravityKVClient(t, kvClient, true, nil)

	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewAntigravityExecutor(cfg)
	auth := newIssue7753AntigravityAuth("issue7753-nonstream-429-kv-err", server.URL)
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

	_, errExec := exec.executeClaudeNonStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatAntigravity,
		ResponseFormat: sdktranslator.FormatAntigravity,
	})
	if errExec == nil {
		t.Fatal("executeClaudeNonStream() = nil, want 503 error")
	}
	sErr, ok := errExec.(statusErr)
	if !ok || sErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("err = %v (%T), want 503 homeKVUnavailableStatusErr", errExec, errExec)
	}
}

func TestAntigravityIssue7753_MidStream429RespectsDisableCooling(t *testing.T) {
	errFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			kvClient := newFakeAntigravityKVClient()
			useFakeAntigravityKVClient(t, kvClient, true, nil)

			cfg := &config.Config{DisableCooling: true}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-disable-cool-"+name, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			req := cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			}
			var gotErr error
			if stream {
				res, errExec := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExec != nil {
					t.Fatalf("ExecuteStream() setup error = %v", errExec)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						gotErr = chunk.Err
					}
				}
			} else {
				_, gotErr = exec.executeClaudeNonStream(context.Background(), auth, req, opts)
			}
			sErr, ok := gotErr.(statusErr)
			if !ok || sErr.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("err = %v (%T), want 429 statusErr", gotErr, gotErr)
			}
			if kvClient.setCount != 0 {
				t.Fatalf("KVSet count = %d, want 0 when DisableCooling is true", kvClient.setCount)
			}
		})
	}
}

func TestAntigravityIssue7753_MidStreamErrorPreservesPriorCreditsFailureState(t *testing.T) {
	errFrame := `{"error":{"code":500,"message":"internal","status":"INTERNAL"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + errFrame + "\n\n"))
	}))
	defer server.Close()

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			cfg := &config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-preserve-credits-"+name, server.URL)
			t.Cleanup(func() {
				closeAntigravityAuthIdleTransports(auth)
				cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
			})

			markAntigravityCreditsPermanentlyDisabled(auth)
			ctx := cliproxyauth.WithAntigravityCredits(context.Background())
			req := cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			}
			if stream {
				res, errExec := exec.ExecuteStream(ctx, auth, req, opts)
				if errExec != nil {
					t.Fatalf("ExecuteStream() setup error = %v", errExec)
				}
				for range res.Chunks {
				}
			} else {
				_, _ = exec.executeClaudeNonStream(ctx, auth, req, opts)
			}
			if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); !loaded {
				t.Fatal("HTTP 200 mid-stream error prematurely cleared prior credits failure state")
			}
		})
	}
}

func TestAntigravityIssue7753_MidStreamNullErrorIgnored(t *testing.T) {
	sseBody := "data: {\"error\":null,\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"null-err-ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			cfg := &config.Config{}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-null-err-"+name, server.URL)
			t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })

			req := cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			}
			if stream {
				res, errExec := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExec != nil {
					t.Fatalf("ExecuteStream() setup error = %v", errExec)
				}
				var out strings.Builder
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
					out.Write(chunk.Payload)
				}
				if !strings.Contains(out.String(), "null-err-ok") {
					t.Fatalf("stream output = %q, want null-err-ok", out.String())
				}
			} else {
				resp, errExec := exec.executeClaudeNonStream(context.Background(), auth, req, opts)
				if errExec != nil {
					t.Fatalf("executeClaudeNonStream() error = %v", errExec)
				}
				if !strings.Contains(string(resp.Payload), "null-err-ok") {
					t.Fatalf("response payload = %q, want null-err-ok", string(resp.Payload))
				}
			}
		})
	}
}

func TestAntigravityIssue7753_MetadataFollowedByMidStream429PreservesCreditsFailureState(t *testing.T) {
	metadataFrame := `{"response":{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"promptTokenCount":10}}}`
	rateLimitFrame := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	sseBody := "data: " + metadataFrame + "\n\n" + "data: " + rateLimitFrame + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			cfg := &config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-meta-then-429-"+name, server.URL)
			t.Cleanup(func() {
				closeAntigravityAuthIdleTransports(auth)
				cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
			})

			markAntigravityCreditsPermanentlyDisabled(auth)
			ctx := cliproxyauth.WithAntigravityCredits(context.Background())
			model := "claude-sonnet-4-6"
			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			}

			var gotErr error
			if stream {
				res, errExec := exec.ExecuteStream(ctx, auth, req, opts)
				if errExec != nil {
					t.Fatalf("ExecuteStream() setup error = %v", errExec)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						gotErr = chunk.Err
					}
				}
			} else {
				_, gotErr = exec.executeClaudeNonStream(ctx, auth, req, opts)
			}

			sErr, ok := gotErr.(statusErr)
			if !ok || sErr.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("err = %v (%T), want 429 statusErr", gotErr, gotErr)
			}
			rawState, loaded := antigravityCreditsFailureByAuth.Load(auth.ID)
			if !loaded {
				t.Fatal("metadata + mid-stream 429 prematurely cleared antigravityCreditsFailureByAuth")
			}
			state, okState := rawState.(antigravityCreditsFailureState)
			if !okState || !state.PermanentlyDisabled || !state.ExplicitBalanceExhausted {
				t.Fatalf("credits failure state = %+v, want PermanentlyDisabled=true ExplicitBalanceExhausted=true", state)
			}
			hint, okHint := cliproxyauth.GetAntigravityCreditsHint(auth.ID)
			if !okHint || !hint.Known || hint.Available {
				t.Fatalf("credits hint = %+v (ok=%v), want Known=true Available=false preserved", hint, okHint)
			}
			inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), auth, model, time.Now())
			if errCooldown != nil {
				t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
			}
			if !inCooldown || remaining <= 0 {
				t.Fatalf("short cooldown = (%v, %s), want active short cooldown", inCooldown, remaining)
			}
			if antigravityTransports.Contains(issue7753TransportKey(cfg, auth)) {
				t.Fatal("expected idle transports for auth to be evicted on mid-stream 429")
			}
		})
	}
}

func TestAntigravityIssue7753_ConfirmedSuccessClearsPriorCreditsFailureState(t *testing.T) {
	sseBody := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)

			cfg := &config.Config{
				QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true},
			}
			exec := NewAntigravityExecutor(cfg)
			auth := newIssue7753AntigravityAuth("issue7753-success-clears-credits-"+name, server.URL)
			t.Cleanup(func() {
				closeAntigravityAuthIdleTransports(auth)
				cliproxyauth.SetAntigravityCreditsHint(auth.ID, cliproxyauth.AntigravityCreditsHint{})
			})

			markAntigravityCreditsPermanentlyDisabled(auth)
			ctx := cliproxyauth.WithAntigravityCredits(context.Background())
			req := cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatAntigravity,
				ResponseFormat: sdktranslator.FormatAntigravity,
			}

			if stream {
				res, errExec := exec.ExecuteStream(ctx, auth, req, opts)
				if errExec != nil {
					t.Fatalf("ExecuteStream() error = %v", errExec)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
				}
			} else {
				if _, errExec := exec.executeClaudeNonStream(ctx, auth, req, opts); errExec != nil {
					t.Fatalf("executeClaudeNonStream() error = %v", errExec)
				}
			}
			if _, loaded := antigravityCreditsFailureByAuth.Load(auth.ID); loaded {
				t.Fatal("confirmed successful credits request should clear prior credits failure state")
			}
		})
	}
}
