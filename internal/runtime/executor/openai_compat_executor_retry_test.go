package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestOpenAICompatExecutorExecute_RetriesTransientNetworkError(t *testing.T) {
	const (
		providerName = "retry-openai-compat-success"
		authID       = "retry-openai-compat-success-auth"
		modelID      = "retry-success-model"
	)

	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if attempts.Add(1) == 1 {
			closeConnectionWithoutResponse(t, w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_retry","object":"chat.completion","created":1775540000,"model":"retry-success-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          1,
		OpenAICompatNetworkRetryBackoffMS: 1,
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           providerName,
			CircuitBreakerFailureThreshold: 1,
		}},
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if reg.IsCircuitOpen(authID, modelID) {
		t.Fatalf("did not expect circuit to open for model %q after successful retry", modelID)
	}
	if !strings.Contains(string(resp.Payload), `"content":"ok"`) {
		t.Fatalf("unexpected response body: %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorExecute_ZeroNetworkRetryDisablesExtraAttempt(t *testing.T) {
	const (
		providerName = "retry-openai-compat-disabled"
		authID       = "retry-openai-compat-disabled-auth"
		modelID      = "retry-disabled-model"
	)

	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		attempts.Add(1)
		closeConnectionWithoutResponse(t, w)
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          0,
		OpenAICompatNetworkRetryBackoffMS: 1,
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected upstream network error with retry disabled")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestOpenAICompatExecutorExecute_RetryExhaustionRecordsSingleFailurePerRequest(t *testing.T) {
	const (
		providerName = "retry-openai-compat-failure"
		authID       = "retry-openai-compat-failure-auth"
		modelID      = "retry-failure-model"
	)

	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		attempts.Add(1)
		closeConnectionWithoutResponse(t, w)
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          1,
		OpenAICompatNetworkRetryBackoffMS: 1,
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           providerName,
			CircuitBreakerFailureThreshold: 2,
		}},
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	request := cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	_, err := executor.Execute(context.Background(), auth, request, opts)
	if err == nil {
		t.Fatal("expected upstream network error on first logical request")
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts after first Execute = %d, want 2", attempts.Load())
	}
	if reg.IsCircuitOpen(authID, modelID) {
		t.Fatalf("did not expect circuit to open after one logical request")
	}

	_, err = executor.Execute(context.Background(), auth, request, opts)
	if err == nil {
		t.Fatal("expected upstream network error on second logical request")
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts after second Execute = %d, want 4", attempts.Load())
	}
	if !reg.IsCircuitOpen(authID, modelID) {
		t.Fatalf("expected circuit to open after two logical requests")
	}
}

func TestOpenAICompatExecutorExecute_DoesNotRetryHTTPStatusError(t *testing.T) {
	const (
		providerName = "retry-openai-compat-http-status"
		authID       = "retry-openai-compat-http-status-auth"
		modelID      = "retry-http-status-model"
	)

	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary upstream error"}}`))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          1,
		OpenAICompatNetworkRetryBackoffMS: 1,
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected upstream status error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestOpenAICompatExecutorExecuteStream_RetriesTransientNetworkError(t *testing.T) {
	const (
		providerName = "retry-openai-compat-stream-success"
		authID       = "retry-openai-compat-stream-success-auth"
		modelID      = "retry-stream-success-model"
	)

	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if attempts.Add(1) == 1 {
			closeConnectionWithoutResponse(t, w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_stream_retry\",\"object\":\"chat.completion.chunk\",\"created\":1775540000,\"model\":\"retry-stream-success-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          1,
		OpenAICompatNetworkRetryBackoffMS: 1,
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"stream":true}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	chunkCount := 0
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		chunkCount++
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if chunkCount == 0 {
		t.Fatal("expected stream chunks after successful retry")
	}
}

func TestOpenAICompatExecutorExecuteStream_DoesNotRetryMidStreamFailure(t *testing.T) {
	const (
		providerName = "retry-openai-compat-midstream"
		authID       = "retry-openai-compat-midstream-auth"
		modelID      = "retry-midstream-model"
	)

	var attempts atomic.Int32
	oversizedLine := "data: " + strings.Repeat("x", 52_430_000) + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(oversizedLine))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          1,
		OpenAICompatNetworkRetryBackoffMS: 1,
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"stream":true}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var gotErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatal("expected mid-stream scan error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestOpenAICompatExecutorExecute_ContextDeadlineStopsRetryWait(t *testing.T) {
	const (
		providerName = "retry-openai-compat-budget"
		authID       = "retry-openai-compat-budget-auth"
		modelID      = "retry-budget-model"
	)

	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		attempts.Add(1)
		closeConnectionWithoutResponse(t, w)
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatNetworkRetry:          1,
		OpenAICompatNetworkRetryBackoffMS: 200,
	})
	auth := retryTestAuth(upstream.URL, providerName, authID)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected context timeout while waiting to retry")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func retryTestAuth(serverURL, providerName, authID string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       authID,
		Provider: providerName,
		Attributes: map[string]string{
			"base_url":     serverURL + "/v1",
			"api_key":      "test-key",
			"compat_name":  providerName,
			"provider_key": providerName,
		},
	}
}

func closeConnectionWithoutResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer does not support hijacking")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("Hijack() error = %v", err)
	}
	_ = conn.Close()
}
