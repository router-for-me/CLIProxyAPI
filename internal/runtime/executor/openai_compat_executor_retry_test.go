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
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestParseHTTPRetryAfter(t *testing.T) {
	now := time.Date(2026, time.May, 31, 14, 0, 0, 0, time.UTC)
	headers := http.Header{}

	headers.Set("Retry-After", "7")
	retryAfter := parseHTTPRetryAfter(headers, now)
	if retryAfter == nil || *retryAfter != 7*time.Second {
		t.Fatalf("numeric retry-after = %v, want %v", retryAfter, 7*time.Second)
	}

	retryAt := now.Add(11 * time.Second).UTC().Format(http.TimeFormat)
	headers.Set("Retry-After", retryAt)
	retryAfter = parseHTTPRetryAfter(headers, now)
	if retryAfter == nil || *retryAfter != 11*time.Second {
		t.Fatalf("date retry-after = %v, want %v", retryAfter, 11*time.Second)
	}

	headers.Set("Retry-After", "invalid")
	if retryAfter := parseHTTPRetryAfter(headers, now); retryAfter != nil {
		t.Fatalf("invalid retry-after = %v, want nil", *retryAfter)
	}
}

func TestOpenAICompatExecutorExecutePropagatesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted"}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "retry-model",
		Payload: []byte(`{"model":"retry-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want 429")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("Execute() retry-after missing: %T %v", err, err)
	}
	if got := *retryable.RetryAfter(); got != 7*time.Second {
		t.Fatalf("Execute() retry-after = %v, want %v", got, 7*time.Second)
	}
}

func TestOpenAICompatExecutorExecuteStreamPropagatesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted"}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "retry-model",
		Payload: []byte(`{"model":"retry-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want 429")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("ExecuteStream() retry-after missing: %T %v", err, err)
	}
	if got := *retryable.RetryAfter(); got != 4*time.Second {
		t.Fatalf("ExecuteStream() retry-after = %v, want %v", got, 4*time.Second)
	}
}
