package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	qwenauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qwen"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "qwen-max", "qwen-max", ""},
		{"with level suffix", "qwen-max(high)", "qwen-max", "high"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max", "16384"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestEnsureQwenSystemMessage_MergeStringSystem(t *testing.T) {
	payload := []byte(`{
		"model": "qwen3.6-plus",
		"stream": true,
		"messages": [
			{ "role": "system", "content": "ABCDEFG" },
			{ "role": "user", "content": [ { "type": "text", "text": "你好" } ] }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	if msgs[0].Get("role").String() != "system" {
		t.Fatalf("messages[0].role = %q, want %q", msgs[0].Get("role").String(), "system")
	}
	parts := msgs[0].Get("content").Array()
	if len(parts) != 2 {
		t.Fatalf("messages[0].content length = %d, want 2", len(parts))
	}
	if parts[0].Get("type").String() != "text" || parts[0].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("messages[0].content[0] = %s, want injected system part", parts[0].Raw)
	}
	if text := parts[0].Get("text").String(); text != "" && text != "You are Qwen Code." {
		t.Fatalf("messages[0].content[0].text = %q, want empty string or default prompt", text)
	}
	if parts[1].Get("type").String() != "text" || parts[1].Get("text").String() != "ABCDEFG" {
		t.Fatalf("messages[0].content[1] = %s, want text part with ABCDEFG", parts[1].Raw)
	}
	if msgs[1].Get("role").String() != "user" {
		t.Fatalf("messages[1].role = %q, want %q", msgs[1].Get("role").String(), "user")
	}
}

func TestEnsureQwenSystemMessage_MergeObjectSystem(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{ "role": "system", "content": { "type": "text", "text": "ABCDEFG" } },
			{ "role": "user", "content": [ { "type": "text", "text": "你好" } ] }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	parts := msgs[0].Get("content").Array()
	if len(parts) != 2 {
		t.Fatalf("messages[0].content length = %d, want 2", len(parts))
	}
	if parts[1].Get("text").String() != "ABCDEFG" {
		t.Fatalf("messages[0].content[1].text = %q, want %q", parts[1].Get("text").String(), "ABCDEFG")
	}
}

func TestEnsureQwenSystemMessage_PrependsWhenMissing(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{ "role": "user", "content": [ { "type": "text", "text": "你好" } ] }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	if msgs[0].Get("role").String() != "system" {
		t.Fatalf("messages[0].role = %q, want %q", msgs[0].Get("role").String(), "system")
	}
	if !msgs[0].Get("content").IsArray() || len(msgs[0].Get("content").Array()) == 0 {
		t.Fatalf("messages[0].content = %s, want non-empty array", msgs[0].Get("content").Raw)
	}
	if msgs[1].Get("role").String() != "user" {
		t.Fatalf("messages[1].role = %q, want %q", msgs[1].Get("role").String(), "user")
	}
}

func TestEnsureQwenSystemMessage_MergesMultipleSystemMessages(t *testing.T) {
	payload := []byte(`{
		"messages": [
			{ "role": "system", "content": "A" },
			{ "role": "user", "content": [ { "type": "text", "text": "hi" } ] },
			{ "role": "system", "content": "B" }
		]
	}`)

	out, err := ensureQwenSystemMessage(payload)
	if err != nil {
		t.Fatalf("ensureQwenSystemMessage() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2", len(msgs))
	}
	parts := msgs[0].Get("content").Array()
	if len(parts) != 3 {
		t.Fatalf("messages[0].content length = %d, want 3", len(parts))
	}
	if parts[1].Get("text").String() != "A" {
		t.Fatalf("messages[0].content[1].text = %q, want %q", parts[1].Get("text").String(), "A")
	}
	if parts[2].Get("text").String() != "B" {
		t.Fatalf("messages[0].content[2].text = %q, want %q", parts[2].Get("text").String(), "B")
	}
}

func TestWrapQwenError_InsufficientQuotaDoesNotSetRetryAfter(t *testing.T) {
	body := []byte(`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota","type":"insufficient_quota"}}`)
	code, retryAfter := wrapQwenError(context.Background(), http.StatusTooManyRequests, body)
	if code != http.StatusTooManyRequests {
		t.Fatalf("wrapQwenError status = %d, want %d", code, http.StatusTooManyRequests)
	}
	if retryAfter != nil {
		t.Fatalf("wrapQwenError retryAfter = %v, want nil", *retryAfter)
	}
}

func TestWrapQwenError_Maps403QuotaTo429WithoutRetryAfter(t *testing.T) {
	body := []byte(`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota","type":"insufficient_quota"}}`)
	code, retryAfter := wrapQwenError(context.Background(), http.StatusForbidden, body)
	if code != http.StatusTooManyRequests {
		t.Fatalf("wrapQwenError status = %d, want %d", code, http.StatusTooManyRequests)
	}
	if retryAfter != nil {
		t.Fatalf("wrapQwenError retryAfter = %v, want nil", *retryAfter)
	}
}

func TestQwenCreds_NormalizesResourceURL(t *testing.T) {
	tests := []struct {
		name        string
		resourceURL string
		wantBaseURL string
	}{
		{"host only", "portal.qwen.ai", "https://portal.qwen.ai/v1"},
		{"scheme no v1", "https://portal.qwen.ai", "https://portal.qwen.ai/v1"},
		{"scheme with v1", "https://portal.qwen.ai/v1", "https://portal.qwen.ai/v1"},
		{"scheme with v1 slash", "https://portal.qwen.ai/v1/", "https://portal.qwen.ai/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{
				Metadata: map[string]any{
					"access_token": "test-token",
					"resource_url": tt.resourceURL,
				},
			}

			token, baseURL := qwenCreds(auth)
			if token != "test-token" {
				t.Fatalf("qwenCreds token = %q, want %q", token, "test-token")
			}
			if baseURL != tt.wantBaseURL {
				t.Fatalf("qwenCreds baseURL = %q, want %q", baseURL, tt.wantBaseURL)
			}
		})
	}
}

func TestQwenExecutorExecute_429RefreshAndRetry(t *testing.T) {
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests = make(map[string][]time.Time)
	qwenRateLimiter.Unlock()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer old-token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded","message":"quota exceeded","type":"quota_exceeded"}}`))
			return
		case "Bearer new-token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"qwen-max","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}))
	defer srv.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "auth-test",
		Provider: "qwen",
		Attributes: map[string]string{
			"base_url": srv.URL + "/v1",
		},
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
		},
	}

	var refresherCalls int32
	exec.refreshForImmediateRetry = func(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
		atomic.AddInt32(&refresherCalls, 1)
		refreshed := auth.Clone()
		if refreshed.Metadata == nil {
			refreshed.Metadata = make(map[string]any)
		}
		refreshed.Metadata["access_token"] = "new-token"
		refreshed.Metadata["refresh_token"] = "refresh-token-2"
		return refreshed, nil
	}
	ctx := context.Background()

	resp, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("Execute() payload is empty")
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2", atomic.LoadInt32(&calls))
	}
	if atomic.LoadInt32(&refresherCalls) != 1 {
		t.Fatalf("refresher calls = %d, want 1", atomic.LoadInt32(&refresherCalls))
	}
}

func TestQwenExecutorExecuteStream_429RefreshAndRetry(t *testing.T) {
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests = make(map[string][]time.Time)
	qwenRateLimiter.Unlock()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer old-token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"quota_exceeded","message":"quota exceeded","type":"quota_exceeded"}}`))
			return
		case "Bearer new-token":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qwen-max\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}))
	defer srv.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "auth-test",
		Provider: "qwen",
		Attributes: map[string]string{
			"base_url": srv.URL + "/v1",
		},
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-token",
		},
	}

	var refresherCalls int32
	exec.refreshForImmediateRetry = func(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
		atomic.AddInt32(&refresherCalls, 1)
		refreshed := auth.Clone()
		if refreshed.Metadata == nil {
			refreshed.Metadata = make(map[string]any)
		}
		refreshed.Metadata["access_token"] = "new-token"
		refreshed.Metadata["refresh_token"] = "refresh-token-2"
		return refreshed, nil
	}
	ctx := context.Background()

	stream, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2", atomic.LoadInt32(&calls))
	}
	if atomic.LoadInt32(&refresherCalls) != 1 {
		t.Fatalf("refresher calls = %d, want 1", atomic.LoadInt32(&refresherCalls))
	}

	var sawPayload bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			sawPayload = true
		}
	}
	if !sawPayload {
		t.Fatalf("stream did not produce any payload chunks")
	}
}

func TestQwenExecutorRefresh_UpdatesStorage(t *testing.T) {
	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "qwen",
		Metadata: map[string]any{
			"refresh_token": "old-refresh-token",
		},
		Storage: &qwenauth.QwenTokenStorage{
			AccessToken:  "old-access-token",
			RefreshToken: "old-refresh-token",
			ResourceURL:  "https://portal.qwen.ai",
			Expire:       "2000-01-01T00:00:00Z",
			LastRefresh:  "2000-01-01T00:00:00Z",
		},
	}

	originalStorage := auth.Storage

	exec.refreshTokens = func(ctx context.Context, refreshToken string) (*qwenauth.QwenTokenData, error) {
		return &qwenauth.QwenTokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			TokenType:    "Bearer",
			ResourceURL:  "https://portal.qwen.ai",
			Expire:       time.Now().Add(time.Hour).Format(time.RFC3339),
		}, nil
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if refreshed == nil {
		t.Fatal("Refresh() returned nil auth")
	}

	if got, _ := refreshed.Metadata["access_token"].(string); got != "new-access-token" {
		t.Fatalf("metadata access_token = %q, want %q", got, "new-access-token")
	}
	if got, _ := refreshed.Metadata["refresh_token"].(string); got != "new-refresh-token" {
		t.Fatalf("metadata refresh_token = %q, want %q", got, "new-refresh-token")
	}

	storage, ok := refreshed.Storage.(*qwenauth.QwenTokenStorage)
	if !ok || storage == nil {
		t.Fatalf("storage type = %T, want *qwenauth.QwenTokenStorage", refreshed.Storage)
	}
	if storage.AccessToken != "new-access-token" {
		t.Fatalf("storage access_token = %q, want %q", storage.AccessToken, "new-access-token")
	}
	if storage.RefreshToken != "new-refresh-token" {
		t.Fatalf("storage refresh_token = %q, want %q", storage.RefreshToken, "new-refresh-token")
	}

	// Verify the refreshed storage is a new instance (not the original pointer)
	if refreshed.Storage == originalStorage {
		t.Fatal("refreshed Storage should be a new cloned instance, not the original pointer")
	}
}

func TestQwenExecutorRefresh_UpdatesStorageMetadataOnly(t *testing.T) {
	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "qwen",
		Metadata: map[string]any{
			"refresh_token": "old-refresh-token",
		},
		Storage: nil, // Test without Storage struct
	}

	exec.refreshTokens = func(ctx context.Context, refreshToken string) (*qwenauth.QwenTokenData, error) {
		return &qwenauth.QwenTokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			Expire:       time.Now().Add(time.Hour).Format(time.RFC3339),
		}, nil
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if got, _ := refreshed.Metadata["access_token"].(string); got != "new-access-token" {
		t.Fatalf("metadata access_token = %q, want %q", got, "new-access-token")
	}
	if got, _ := refreshed.Metadata["refresh_token"].(string); got != "new-refresh-token" {
		t.Fatalf("metadata refresh_token = %q, want %q", got, "new-refresh-token")
	}
}

func TestQwenExecutorRefresh_PreservesExistingStorageWhenResponseHasEmptyFields(t *testing.T) {
	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "qwen",
		Metadata: map[string]any{
			"refresh_token": "old-refresh-token",
		},
		Storage: &qwenauth.QwenTokenStorage{
			AccessToken:  "old-access-token",
			RefreshToken: "old-refresh-token",
			ResourceURL:  "https://portal.qwen.ai",
			Expire:       "2000-01-01T00:00:00Z",
			LastRefresh:  "2000-01-01T00:00:00Z",
		},
	}

	// Simulate a refresh response that omits refresh_token and resource_url
	exec.refreshTokens = func(ctx context.Context, refreshToken string) (*qwenauth.QwenTokenData, error) {
		return &qwenauth.QwenTokenData{
			AccessToken: "new-access-token",
			// RefreshToken and ResourceURL intentionally empty
			Expire: time.Now().Add(time.Hour).Format(time.RFC3339),
		}, nil
	}

	refreshed, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	storage, ok := refreshed.Storage.(*qwenauth.QwenTokenStorage)
	if !ok || storage == nil {
		t.Fatalf("storage type = %T, want *qwenauth.QwenTokenStorage", refreshed.Storage)
	}

	// AccessToken should be updated
	if storage.AccessToken != "new-access-token" {
		t.Fatalf("storage access_token = %q, want %q", storage.AccessToken, "new-access-token")
	}

	// RefreshToken and ResourceURL should be preserved from the original
	if storage.RefreshToken != "old-refresh-token" {
		t.Fatalf("storage refresh_token = %q, want %q (should be preserved)", storage.RefreshToken, "old-refresh-token")
	}
	if storage.ResourceURL != "https://portal.qwen.ai" {
		t.Fatalf("storage resource_url = %q, want %q (should be preserved)", storage.ResourceURL, "https://portal.qwen.ai")
	}
}
