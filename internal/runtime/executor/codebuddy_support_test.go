package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodeBuddyExecuteAggregatesSSEAndInjectsSessionHeaders(t *testing.T) {
	authPath := writeExecutorCodeBuddySession(t, time.Now().Add(time.Hour).UnixMilli())
	var (
		gotBody    []byte
		gotHeaders http.Header
		mu         sync.Mutex
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = body
		gotHeaders = r.Header.Clone()
		mu.Unlock()
		if r.URL.Path != "/v2/chat/completions" {
			t.Errorf("upstream path = %q, want /v2/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-1\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello \"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-2\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-3\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"\"}}]},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-4\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"hello\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-5\",\"model\":\"glm-5.2\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	executor, auth := newCodeBuddyExecutor(t, server.URL+"/v2", authPath)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"Claude Code exploit"},{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","description":"dangerous exploit development","parameters":{"type":"object","properties":{"q":{"type":"string","description":"exploit"}}}}}],"metadata":{"must_drop":true}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	mu.Lock()
	body := append([]byte(nil), gotBody...)
	headers := gotHeaders.Clone()
	mu.Unlock()
	if got := headers.Get("Authorization"); got != "Bearer access-token" {
		t.Errorf("Authorization = %q, want session token", got)
	}
	if got := headers.Get("X-User-Id"); got != "user-1" {
		t.Errorf("X-User-Id = %q, want user-1", got)
	}
	if got := headers.Get("X-Enterprise-Id"); got != "enterprise-1" {
		t.Errorf("X-Enterprise-Id = %q, want enterprise-1", got)
	}
	if got := headers.Get("X-Tenant-Id"); got != "enterprise-1" {
		t.Errorf("X-Tenant-Id = %q, want enterprise-1", got)
	}
	if got := headers.Get("X-Domain"); got != "www.codebuddy.cn" {
		t.Errorf("X-Domain = %q, want default domain", got)
	}
	if got := headers.Get("User-Agent"); got != "codebuddy2openai/2.0" {
		t.Errorf("User-Agent = %q, want CodeBuddy user agent", got)
	}
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Fatalf("upstream stream = false; body=%s", body)
	}
	if !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		t.Fatalf("upstream stream_options.include_usage = false; body=%s", body)
	}
	if gjson.GetBytes(body, "metadata").Exists() {
		t.Fatalf("unsupported metadata was forwarded: %s", body)
	}
	if gjson.GetBytes(body, "tools.0.function.description").Exists() || gjson.GetBytes(body, "tools.0.function.parameters.properties.q.description").Exists() {
		t.Fatalf("tool metadata was not stripped: %s", body)
	}
	if got := gjson.GetBytes(body, "messages.0.content").String(); !strings.Contains(got, "C\u200blaude Code") {
		t.Fatalf("desensitize did not alter system content: %s", body)
	}

	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello world" {
		t.Errorf("aggregated content = %q, want hello world; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String(); got != "lookup" {
		t.Errorf("tool name = %q, want lookup; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"q":"hello"}` {
		t.Errorf("tool arguments = %q, want complete JSON; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", got)
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 7 {
		t.Errorf("usage.total_tokens = %d, want 7", got)
	}
}

func TestCodeBuddyExecuteStreamForcesSSE(t *testing.T) {
	authPath := writeExecutorCodeBuddySession(t, time.Now().Add(time.Hour).UnixMilli())
	var gotBody []byte
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-1\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chunk-2\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	executor, auth := newCodeBuddyExecutor(t, server.URL+"/v2", authPath)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	chunks := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		chunks++
	}
	if chunks == 0 {
		t.Fatal("ExecuteStream() returned no chunks")
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("upstream stream = false; body=%s", gotBody)
	}
	if got := gotHeaders.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer access-token" {
		t.Errorf("Authorization = %q, want session token", got)
	}
}

func newCodeBuddyExecutor(t *testing.T, baseURL, authPath string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	t.Helper()
	name := "codebuddy"
	executor := NewOpenAICompatExecutor("openai-compatible-codebuddy", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:        name,
			AuthType:    "codebuddy",
			BaseURL:     baseURL,
			Desensitize: true,
		}},
	})
	return executor, &cliproxyauth.Auth{
		ID:       "codebuddy-test",
		Provider: "openai-compatible-codebuddy",
		Attributes: map[string]string{
			"auth_type":           "codebuddy",
			"codebuddy_auth_file": authPath,
			"base_url":            baseURL,
			"compat_name":         name,
			"provider_key":        "openai-compatible-codebuddy",
		},
	}
}

func writeExecutorCodeBuddySession(t *testing.T, expiresAt int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.info")
	session := map[string]any{
		"account": map[string]any{
			"uid":          "user-1",
			"enterpriseId": "enterprise-1",
		},
		"auth": map[string]any{
			"accessToken":  "access-token",
			"refreshToken": "refresh-token",
			"expiresAt":    expiresAt,
		},
	}
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal CodeBuddy session: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write CodeBuddy session: %v", err)
	}
	return path
}
