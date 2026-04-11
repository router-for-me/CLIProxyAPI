package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

func TestQwenExecutorExecute_429DoesNotRefreshOrRetry(t *testing.T) {
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

	_, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatalf("Execute() expected error, got nil")
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("Execute() error type = %T, want statusErr", err)
	}
	if status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("Execute() status code = %d, want %d", status.StatusCode(), http.StatusTooManyRequests)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", atomic.LoadInt32(&calls))
	}
	if atomic.LoadInt32(&refresherCalls) != 0 {
		t.Fatalf("refresher calls = %d, want 0", atomic.LoadInt32(&refresherCalls))
	}
}

func TestQwenExecutorExecuteStream_429DoesNotRefreshOrRetry(t *testing.T) {
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

	_, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatalf("ExecuteStream() expected error, got nil")
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("ExecuteStream() error type = %T, want statusErr", err)
	}
	if status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("ExecuteStream() status code = %d, want %d", status.StatusCode(), http.StatusTooManyRequests)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", atomic.LoadInt32(&calls))
	}
	if atomic.LoadInt32(&refresherCalls) != 0 {
		t.Fatalf("refresher calls = %d, want 0", atomic.LoadInt32(&refresherCalls))
	}
}
