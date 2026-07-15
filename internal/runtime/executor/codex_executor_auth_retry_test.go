package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
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

func TestCodexExecutorExecuteRefreshRetryTransportErrorDoesNotPanic(t *testing.T) {
	calls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer stale-token" {
			t.Fatalf("first Authorization = %q, want stale token", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"code":"token_invalidated","message":"Your authentication token has been invalidated. Please try signing in again."},"status":401}`)
	}))
	defer server.Close()

	oldRefresh := refreshCodexAuthForTokenInvalidatedRetry
	refreshCodexAuthForTokenInvalidatedRetry = func(ctx context.Context, e *CodexExecutor, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
		auth.Metadata["access_token"] = "fresh-token"
		server.Close()
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
	if err == nil {
		t.Fatal("expected retry transport error, got nil")
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 before retry transport failure", calls)
	}
}

func TestCodexExecutorExecuteRefreshRetryClearsInvalidReasoningReplay(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	const sessionID = "retry-invalid-signature"
	const sessionKey = "execution:" + sessionID
	model := "gpt-5.5"
	if !internalcache.CacheCodexReasoningReplayItem(model, sessionKey, validCodexReasoningReplayItemForAuthRetryTest(7)) {
		t.Fatal("failed to seed codex reasoning replay cache")
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":{"code":"token_invalidated","message":"Your authentication token has been invalidated. Please try signing in again."},"status":401}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}`)
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
		Model:    model,
		Payload:  []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`),
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID},
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err == nil {
		t.Fatal("expected retry invalid signature error, got nil")
	}
	if _, ok := internalcache.GetCodexReasoningReplayItem(model, sessionKey); ok {
		t.Fatal("expected invalid reasoning replay cache to be cleared after retry error")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

func validCodexReasoningReplayItemForAuthRetryTest(seed byte) []byte {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = seed + byte(i)
	}
	encryptedContent := base64.RawURLEncoding.EncodeToString(payload)
	return []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + encryptedContent + `"}`)
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
