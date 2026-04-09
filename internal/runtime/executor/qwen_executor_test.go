package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func clearQwenRateLimiter() {
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests = make(map[string][]time.Time)
	qwenRateLimiter.Unlock()
}

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantBase string
	}{
		{"no suffix", "qwen-max", "qwen-max"},
		{"with level suffix", "qwen-max(high)", "qwen-max"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest"},
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

func TestQwenExecutorExecuteUsesQwenV2ChatEndpoint(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotCookie string
	var gotBody []byte
	var chatsNewCalls int
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/chats/new":
			chatsNewCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"chat-1"}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCookie = r.Header.Get("Cookie")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response.created\":{\"chat_id\":\"chat-1\",\"response_id\":\"resp-1\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"response.stopped\":true}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-v2-success",
		Metadata: map[string]any{
			"token_cookie":    "token-cookie",
			"session_cookies": map[string]any{"refresh_token": "refresh"},
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if chatsNewCalls != 1 {
		t.Fatalf("chats new calls = %d, want 1", chatsNewCalls)
	}
	if gotPath != "/api/v2/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/v2/chat/completions")
	}
	if !strings.Contains(gotCookie, "token=token-cookie") {
		t.Fatalf("cookie header = %q, want token cookie", gotCookie)
	}
	if got := gjson.GetBytes(gotBody, "chat_id").String(); got != "chat-1" {
		t.Fatalf("chat_id = %q, want %q", got, "chat-1")
	}
	if !strings.Contains(gotQuery, "chat_id=chat-1") {
		t.Fatalf("query = %q, want chat_id query parameter", gotQuery)
	}
	if gjson.GetBytes(gotBody, "model").String() != "qwen3.6-plus" {
		t.Fatalf("model = %q, want %q", gjson.GetBytes(gotBody, "model").String(), "qwen3.6-plus")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != "hello" {
		t.Fatalf("messages[0].content = %q, want %q", got, "hello")
	}
	if gjson.GetBytes(gotBody, "query").Exists() {
		t.Fatalf("legacy query field should not be present: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.0.models.0").String(); got != "qwen3.6-plus" {
		t.Fatalf("messages[0].models[0] = %q, want %q", got, "qwen3.6-plus")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.user_action").String(); got != "chat" {
		t.Fatalf("messages[0].user_action = %q, want %q", got, "chat")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.feature_config.output_schema").String(); got != "phase" {
		t.Fatalf("messages[0].feature_config.output_schema = %q, want %q", got, "phase")
	}
	if got := gjson.GetBytes(gotBody, "version").String(); got != "2.1" {
		t.Fatalf("version = %q, want %q", got, "2.1")
	}
	if !gjson.GetBytes(gotBody, "incremental_output").Bool() {
		t.Fatalf("incremental_output = false, want true")
	}
	if got := gjson.GetBytes(gotBody, "chat_mode").String(); got != "normal" {
		t.Fatalf("chat_mode = %q, want %q", got, "normal")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.chat_type").String(); got != "t2t" {
		t.Fatalf("messages[0].chat_type = %q, want %q", got, "t2t")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.sub_chat_type").String(); got != "t2t" {
		t.Fatalf("messages[0].sub_chat_type = %q, want %q", got, "t2t")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.feature_config.thinking_enabled").Bool(); !got {
		t.Fatalf("messages[0].feature_config.thinking_enabled = %v, want true", got)
	}
	if got := gjson.GetBytes(gotBody, "messages.0.feature_config.auto_thinking").Bool(); !got {
		t.Fatalf("messages[0].feature_config.auto_thinking = %v, want true", got)
	}
	if got := gjson.GetBytes(gotBody, "messages.0.feature_config.auto_search").Bool(); !got {
		t.Fatalf("messages[0].feature_config.auto_search = %v, want true", got)
	}
	if got := gjson.GetBytes(gotBody, "messages.0.extra.meta.subChatType").String(); got != "t2t" {
		t.Fatalf("messages[0].extra.meta.subChatType = %q, want %q", got, "t2t")
	}
}

func TestQwenExecutorExecuteAggregatesSSEIntoOpenAIResponse(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/chats/new":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"chat-agg"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response.created\":{\"chat_id\":\"chat-agg\",\"response_id\":\"resp-agg\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"he\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n"))
		_, _ = w.Write([]byte("data: {\"response.stopped\":true}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-nonstream-aggregate",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.role").String(); got != "assistant" {
		t.Fatalf("choices[0].message.role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("choices[0].message.content = %q, want %q", got, "hello")
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("choices[0].finish_reason = %q, want %q", got, "stop")
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 4 {
		t.Fatalf("usage.total_tokens = %d, want %d", got, 4)
	}
}

func TestQwenExecutorExecuteFailsWithoutTokenCookie(t *testing.T) {
	called := false
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-no-token",
		Metadata: map[string]any{
			// token_cookie intentionally missing
			"session_cookies": map[string]any{"refresh_token": "refresh"},
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})

	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	if called {
		t.Fatal("Execute() should fail before issuing upstream request when token_cookie is missing")
	}
	if !strings.Contains(err.Error(), "token_cookie") {
		t.Fatalf("error = %q, want mention token_cookie", err.Error())
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("StatusCode() = %d, want 401", se.StatusCode())
	}
	// Missing token_cookie is a fast-fail validation and should not consume a rate limit slot.
	qwenRateLimiter.Lock()
	_, ok = qwenRateLimiter.requests[auth.ID]
	qwenRateLimiter.Unlock()
	if ok {
		t.Fatalf("rate limiter should not be touched when token_cookie missing (authID=%q)", auth.ID)
	}
}

func TestQwenExecutorExecuteCoderModelUsesLegacyChatCompletions(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody []byte
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-coder-model-legacy",
		Metadata: map[string]any{
			"access_token": "access-token",
		},
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "coder-model",
		Payload: []byte(`{"model":"coder-model","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if gotAuth != "Bearer access-token" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer access-token")
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "coder-model" {
		t.Fatalf("model = %q, want %q", got, "coder-model")
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want %q", got, "ok")
	}
}

func TestQwenExecutorExecuteStreamCoderModelUsesLegacyChatCompletions(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody []byte
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-coder-model-legacy-stream",
		Metadata: map[string]any{
			"access_token": "access-token",
		},
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
		},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "coder-model",
		Payload: []byte(`{"model":"coder-model","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if gotAuth != "Bearer access-token" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer access-token")
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "coder-model" {
		t.Fatalf("model = %q, want %q", got, "coder-model")
	}

	payloads, errs := drainStreamChunks(t, result.Chunks, 2*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected stream errors: %v", errs)
	}
	if len(payloads) != 2 {
		t.Fatalf("payload count = %d, want 2", len(payloads))
	}
	if !strings.Contains(payloads[0], "\"choices\"") {
		t.Fatalf("first payload = %q, want normalized openai chunk", payloads[0])
	}
	if strings.HasPrefix(payloads[0], "data: ") {
		t.Fatalf("first payload = %q, should not keep upstream data prefix", payloads[0])
	}
	if payloads[1] != "[DONE]" {
		t.Fatalf("last payload = %q, want %q", payloads[1], "[DONE]")
	}
}

func TestQwenExecutorExecuteMissingTokenCookieNotBlockedByRateLimit(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-no-token-rate-limit",
		Metadata: map[string]any{
			// token_cookie intentionally missing
			"session_cookies": map[string]any{"refresh_token": "refresh"},
		},
		Attributes: map[string]string{
			"base_url": "https://example.invalid",
		},
	}

	// Pre-fill the limiter to the max for this authID. Missing token_cookie should
	// still return 401 instead of a 429.
	now := time.Now()
	ts := make([]time.Time, 0, qwenRateLimitPerMin)
	for i := 0; i < qwenRateLimitPerMin; i++ {
		ts = append(ts, now.Add(-10*time.Second).Add(time.Duration(i)*time.Millisecond))
	}
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests[auth.ID] = ts
	qwenRateLimiter.Unlock()

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("StatusCode() = %d, want 401 (not 429)", se.StatusCode())
	}
	if !strings.Contains(err.Error(), "token_cookie") {
		t.Fatalf("error = %q, want mention token_cookie", err.Error())
	}

	// Rate limiter should not be incremented by invalid request.
	qwenRateLimiter.Lock()
	got := len(qwenRateLimiter.requests[auth.ID])
	qwenRateLimiter.Unlock()
	if got != qwenRateLimitPerMin {
		t.Fatalf("rate limiter count = %d, want %d", got, qwenRateLimitPerMin)
	}
}

func TestQwenExecutorExecuteReturnsErrorWhenSetModelFails(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-invalid-json",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": "https://example.invalid",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "qwen3.6-plus",
		// Non-object JSON should make sjson.SetBytes(body, "model", ...) fail.
		Payload: []byte(`[]`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "set model") {
		t.Fatalf("error = %q, want mention set model", err.Error())
	}
}

func TestQwenExecutorExecuteUsesDefaultV2BaseURLWhenResourceURLPortal(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	rt := &recordingRoundTripper{
		resps: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":{"id":"chat-default"}}`)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("data: {\"response.created\":{\"chat_id\":\"chat-default\",\"response_id\":\"resp-default\"}}\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")),
			},
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-default-base-url",
		Metadata: map[string]any{
			"token_cookie":    "token-cookie",
			"session_cookies": map[string]any{"refresh_token": "refresh"},
			"resource_url":    "portal.qwen.ai",
		},
	}

	_, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if rt.req == nil {
		t.Fatal("wanted recorded request, got nil")
	}
	if got := rt.req.URL.Scheme + "://" + rt.req.URL.Host + rt.req.URL.Path; got != "https://chat.qwen.ai/api/v2/chat/completions" {
		t.Fatalf("request URL = %q, want %q", got, "https://chat.qwen.ai/api/v2/chat/completions")
	}
	if rt.req.URL.Query().Get("chat_id") != "chat-default" {
		t.Fatalf("request URL query = %q, want chat-default chat_id", rt.req.URL.RawQuery)
	}
}

func TestQwenExecutorExecuteReturnsErrorWhenCoderRespondsWithJSONErrorBody(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/chats/new":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"chat-json-error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"request_id":"req-1","data":{"code":"Bad_Request","details":"Invalid input the coder task debug-1 is not exist."}}`))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-json-error",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusBadRequest {
		t.Fatalf("StatusCode() = %d, want %d", se.StatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "coder task") {
		t.Fatalf("error = %q, want coder task details", err.Error())
	}
}

func TestQwenExecutorExecuteStreamReturnsErrorWhenCoderRespondsWithJSONErrorBody(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/coder/api/v2/environments/":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"environments":[{"id":"env-1"}],"total":1}}`))
			return
		case "/coder/api/v2/task/new":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"task-stream-json-error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"request_id":"req-2","data":{"code":"RequestValidationError","details":"[\"Field 'env_id': Field required\"]"}}`))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-stream-json-error",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("ExecuteStream() expected error, got nil")
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusBadRequest {
		t.Fatalf("StatusCode() = %d, want %d", se.StatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "env_id") {
		t.Fatalf("error = %q, want env_id details", err.Error())
	}
}

func TestCheckQwenRateLimitUsesAnonymousBucketForEmptyAuthID(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	if err := checkQwenRateLimit(""); err != nil {
		t.Fatalf("checkQwenRateLimit() error = %v", err)
	}

	qwenRateLimiter.Lock()
	got := len(qwenRateLimiter.requests[qwenAnonymousAuthID])
	qwenRateLimiter.Unlock()
	if got != 1 {
		t.Fatalf("anonymous bucket count = %d, want 1", got)
	}
}

func TestCheckQwenRateLimitPrunesExpiredEntriesWhenLimited(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	now := time.Now()
	var timestamps []time.Time
	for i := 0; i < qwenRateLimitPerMin; i++ {
		timestamps = append(timestamps, now.Add(-10*time.Second))
	}
	timestamps = append(timestamps, now.Add(-2*qwenRateLimitWindow))

	qwenRateLimiter.Lock()
	qwenRateLimiter.requests["qwen-prune"] = timestamps
	qwenRateLimiter.Unlock()

	err := checkQwenRateLimit("qwen-prune")
	if err == nil {
		t.Fatal("expected rate limit error, got nil")
	}

	qwenRateLimiter.Lock()
	got := len(qwenRateLimiter.requests["qwen-prune"])
	qwenRateLimiter.Unlock()
	if got != qwenRateLimitPerMin {
		t.Fatalf("pruned bucket size = %d, want %d", got, qwenRateLimitPerMin)
	}
}

func TestQwenExecutorExecuteStreamReturnsErrorWhenSetModelFails(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-stream-invalid-json",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": "https://example.invalid",
		},
	}

	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`[]`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("ExecuteStream() expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "set stream model") {
		t.Fatalf("error = %q, want mention set stream model", err.Error())
	}
}

type recordingRoundTripper struct {
	req   *http.Request
	resp  *http.Response
	resps []*http.Response
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.req = req
	if len(r.resps) > 0 {
		resp := r.resps[0]
		r.resps = r.resps[1:]
		return resp, nil
	}
	return r.resp, nil
}

type errorAfterReader struct {
	data      []byte
	offset    int
	injectErr error
	injected  bool
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.offset < len(r.data) {
		n := copy(p, r.data[r.offset:])
		r.offset += n
		return n, nil
	}
	if !r.injected && r.injectErr != nil {
		r.injected = true
		return 0, r.injectErr
	}
	return 0, io.EOF
}

func (r *errorAfterReader) Close() error { return nil }

func drainStreamChunks(t *testing.T, ch <-chan cliproxyexecutor.StreamChunk, timeout time.Duration) (payloads []string, errs []error) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return payloads, errs
			}
			if len(chunk.Payload) > 0 {
				payloads = append(payloads, string(chunk.Payload))
			}
			if chunk.Err != nil {
				errs = append(errs, chunk.Err)
			}
		case <-deadline.C:
			t.Fatalf("timed out draining stream chunks (%v)", timeout)
		}
	}
}

func TestQwenExecutorExecuteStreamUsesQwenV2ChatEndpoint(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotCookie string
	var gotBody []byte
	var chatsNewCalls int
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/chats/new":
			chatsNewCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"chat-stream"}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCookie = r.Header.Get("Cookie")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response.created\":{\"chat_id\":\"chat-stream\",\"response_id\":\"resp-stream\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"response.stopped\":true}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-v2-stream-success",
		Metadata: map[string]any{
			"token_cookie":    "token-cookie",
			"session_cookies": map[string]any{"refresh_token": "refresh"},
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if result == nil {
		t.Fatal("ExecuteStream() returned nil result")
	}
	if got := result.Headers.Get("X-Qwen-Chat-ID"); got == "" {
		t.Fatal("X-Qwen-Chat-ID header missing from stream result")
	}

	_, _ = drainStreamChunks(t, result.Chunks, 2*time.Second)

	if chatsNewCalls != 1 {
		t.Fatalf("chats new calls = %d, want 1", chatsNewCalls)
	}
	if gotPath != "/api/v2/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/v2/chat/completions")
	}
	if !strings.Contains(gotCookie, "token=token-cookie") {
		t.Fatalf("cookie header = %q, want token cookie", gotCookie)
	}
	if got := gjson.GetBytes(gotBody, "chat_id").String(); got != "chat-stream" {
		t.Fatalf("chat_id = %q, want %q", got, "chat-stream")
	}
	if !strings.Contains(gotQuery, "chat_id=chat-stream") {
		t.Fatalf("query = %q, want chat_id query parameter", gotQuery)
	}
	if gjson.GetBytes(gotBody, "model").String() != "qwen3.6-plus" {
		t.Fatalf("model = %q, want %q", gjson.GetBytes(gotBody, "model").String(), "qwen3.6-plus")
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != "hello" {
		t.Fatalf("messages[0].content = %q, want %q", got, "hello")
	}
	if gjson.GetBytes(gotBody, "query").Exists() {
		t.Fatalf("legacy query field should not be present: %s", string(gotBody))
	}
}

func TestQwenExecutorExecuteStreamFiltersNonOpenAIEvents(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/chats/new":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"chat-filter"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response.created\":{\"chat_id\":\"chat-stream\",\"response_id\":\"resp-stream\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"selected_model_id\":\"qwen3.6-plus\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"response.stopped\":true}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-stream-filter-events",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	payloads, errs := drainStreamChunks(t, result.Chunks, 2*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected stream errors: %v", errs)
	}
	if len(payloads) != 2 {
		t.Fatalf("payload count = %d, want 2 (content chunk + [DONE]) payloads=%v", len(payloads), payloads)
	}
	if strings.Contains(payloads[0], "response.created") || strings.Contains(payloads[0], "selected_model_id") {
		t.Fatalf("unexpected non-openai event forwarded: %v", payloads)
	}
	if !strings.Contains(payloads[0], "\"choices\"") {
		t.Fatalf("first payload = %q, want openai chunk", payloads[0])
	}
	if payloads[1] != "[DONE]" {
		t.Fatalf("last payload = %q, want %q", payloads[1], "[DONE]")
	}
}

func TestQwenExecutorExecuteCreatesTaskFromSessionKeyAndCachesIt(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	var chatsNewCalls int
	var completionCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/chats/new":
			chatsNewCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"chat-cached"}}`))
		case "/api/v2/chat/completions":
			completionCalls++
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"response.created\":{\"chat_id\":\"chat-cached\",\"response_id\":\"resp-cached\"}}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-cache-task",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Headers:      http.Header{"X-Client-Request-Id": []string{"session-1"}},
	}
	req := cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if got := qwenCachedTaskID(auth, "session-1"); got == "" {
		t.Fatalf("cached chat id after first Execute() = %q, want non-empty", got)
	}
	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if chatsNewCalls != 1 {
		t.Fatalf("chats new calls = %d, want 1", chatsNewCalls)
	}
	if completionCalls != 2 {
		t.Fatalf("completion calls = %d, want 2", completionCalls)
	}
}

func TestQwenExecutorExecuteReturnsErrorWhenChatCreationFails(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/chats/new" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"data":{"code":"Bad_Request","details":"chat create failed"}}`))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-no-env",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusBadRequest {
		t.Fatalf("StatusCode() = %d, want %d", se.StatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(err.Error(), "chat create failed") {
		t.Fatalf("error = %q, want chat creation failure details", err.Error())
	}
}

func TestQwenExecutorExecuteStreamFailsWithoutTokenCookie(t *testing.T) {
	called := false
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-stream-no-token",
		Metadata: map[string]any{
			// token_cookie intentionally missing
			"session_cookies": map[string]any{"refresh_token": "refresh"},
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("ExecuteStream() expected error, got nil")
	}
	if called {
		t.Fatal("ExecuteStream() should fail before issuing upstream request when token_cookie is missing")
	}
	if !strings.Contains(err.Error(), "token_cookie") {
		t.Fatalf("error = %q, want mention token_cookie", err.Error())
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("StatusCode() = %d, want 401", se.StatusCode())
	}

	// Missing token_cookie is a fast-fail validation and should not consume a rate limit slot.
	qwenRateLimiter.Lock()
	_, ok = qwenRateLimiter.requests[auth.ID]
	qwenRateLimiter.Unlock()
	if ok {
		t.Fatalf("rate limiter should not be touched when token_cookie missing (authID=%q)", auth.ID)
	}
}

func TestQwenExecutorExecuteStreamMissingTokenCookieNotBlockedByRateLimit(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-stream-no-token-rate-limit",
		Metadata: map[string]any{
			// token_cookie intentionally missing
			"session_cookies": map[string]any{"refresh_token": "refresh"},
		},
		Attributes: map[string]string{
			"base_url": "https://example.invalid",
		},
	}

	now := time.Now()
	ts := make([]time.Time, 0, qwenRateLimitPerMin)
	for i := 0; i < qwenRateLimitPerMin; i++ {
		ts = append(ts, now.Add(-10*time.Second).Add(time.Duration(i)*time.Millisecond))
	}
	qwenRateLimiter.Lock()
	qwenRateLimiter.requests[auth.ID] = ts
	qwenRateLimiter.Unlock()

	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("ExecuteStream() expected error, got nil")
	}
	se, ok := err.(cliproxyexecutor.StatusError)
	if !ok {
		t.Fatalf("error type = %T, want cliproxyexecutor.StatusError", err)
	}
	if se.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("StatusCode() = %d, want 401 (not 429)", se.StatusCode())
	}
	if !strings.Contains(err.Error(), "token_cookie") {
		t.Fatalf("error = %q, want mention token_cookie", err.Error())
	}

	qwenRateLimiter.Lock()
	got := len(qwenRateLimiter.requests[auth.ID])
	qwenRateLimiter.Unlock()
	if got != qwenRateLimitPerMin {
		t.Fatalf("rate limiter count = %d, want %d", got, qwenRateLimitPerMin)
	}
}

func TestQwenExecutorExecuteStreamDoesNotSendDoneOnScannerError(t *testing.T) {
	clearQwenRateLimiter()
	t.Cleanup(clearQwenRateLimiter)

	rt := &recordingRoundTripper{
		resps: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":{"id":"chat-scan-error"}}`)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: &errorAfterReader{
					data:      []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"),
					injectErr: errors.New("scanner boom"),
				},
			},
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)

	exec := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-auth-stream-scan-error",
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
		Attributes: map[string]string{
			"base_url": "https://example.invalid",
		},
	}

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "qwen3.6-plus",
		Payload: []byte(`{"model":"qwen3.6-plus","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if result == nil {
		t.Fatal("ExecuteStream() returned nil result")
	}

	payloads, errs := drainStreamChunks(t, result.Chunks, 2*time.Second)
	if len(errs) == 0 {
		t.Fatalf("expected at least one error chunk, got payloads=%v", payloads)
	}
	for _, p := range payloads {
		if strings.Contains(p, "[DONE]") {
			t.Fatalf("unexpected [DONE] in payloads when scanner error occurs: %v", payloads)
		}
	}
}
