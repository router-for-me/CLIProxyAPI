package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorExecuteRefreshesAndRetriesTokenInvalidated(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer stale-token" {
				t.Fatalf("first Authorization = %q, want stale token", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":{"message":"Your authentication token has been invalidated. Please try signing in again.","type":"invalid_request_error","code":"token_invalidated"},"status":401}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Fatalf("retry Authorization = %q, want fresh token", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
	}))
	defer server.Close()

	oldRefresh := refreshCodexAuthForTokenInvalidatedRetry
	refreshCodexAuthForTokenInvalidatedRetry = func(ctx context.Context, e *CodexExecutor, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
		auth.Metadata["access_token"] = "fresh-token"
		return auth, nil
	}
	defer func() { refreshCodexAuthForTokenInvalidatedRetry = oldRefresh }()

	executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Metadata:   map[string]any{"access_token": "stale-token", "refresh_token": "refresh-token"},
		Attributes: map[string]string{"base_url": server.URL},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute returned error after refresh retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

func TestCodexExecutorExecuteStreamRefreshesAndRetriesTokenInvalidated(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer stale-token" {
				t.Fatalf("first Authorization = %q, want stale token", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":{"code":"token_invalidated","message":"Your authentication token has been invalidated. Please try signing in again."},"status":401}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Fatalf("retry Authorization = %q, want fresh token", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
	}))
	defer server.Close()

	oldRefresh := refreshCodexAuthForTokenInvalidatedRetry
	refreshCodexAuthForTokenInvalidatedRetry = func(ctx context.Context, e *CodexExecutor, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
		auth.Metadata["access_token"] = "fresh-token"
		return auth, nil
	}
	defer func() { refreshCodexAuthForTokenInvalidatedRetry = oldRefresh }()

	executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Metadata:   map[string]any{"access_token": "stale-token", "refresh_token": "refresh-token"},
		Attributes: map[string]string{"base_url": server.URL},
	}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("ExecuteStream returned error after refresh retry: %v", err)
	}
	for range result.Chunks {
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

func TestIsCodexTokenInvalidatedResponse(t *testing.T) {
	body := []byte(`{"error":{"code":"token_invalidated","message":"Your authentication token has been invalidated. Please try signing in again."},"status":401}`)
	if !isCodexTokenInvalidatedResponse(http.StatusUnauthorized, body) {
		t.Fatal("expected token_invalidated response to be detected")
	}
	if isCodexTokenInvalidatedResponse(http.StatusTooManyRequests, body) {
		t.Fatal("429 must not be treated as token_invalidated")
	}
	if isCodexTokenInvalidatedResponse(http.StatusUnauthorized, []byte(strings.Repeat("x", 10))) {
		t.Fatal("generic 401 must not be treated as token_invalidated")
	}
}
