package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGitLabExecutorExecuteUsesChatEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gitLabChatEndpoint {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`"chat response"`))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":     srv.URL,
			"access_token": "oauth-access",
			"model_name":   "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":"hello"}]}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "chat response" {
		t.Fatalf("expected chat response, got %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "claude-sonnet-4-5" {
		t.Fatalf("expected resolved model, got %q", got)
	}
}

func TestGitLabExecutorExecuteFallsBackToCodeSuggestions(t *testing.T) {
	chatCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case gitLabChatEndpoint:
			chatCalls++
			http.Error(w, "feature unavailable", http.StatusForbidden)
		case gitLabCodeSuggestionsEndpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"text": "fallback response",
				}},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":              srv.URL,
			"personal_access_token": "glpat-token",
			"auth_method":           "pat",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":"write code"}]}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if chatCalls != 1 {
		t.Fatalf("expected chat endpoint to be tried once, got %d", chatCalls)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "fallback response" {
		t.Fatalf("expected fallback response, got %q", got)
	}
}

func TestGitLabExecutorExecuteUsesAnthropicGateway(t *testing.T) {
	var gotAuthHeader, gotRealmHeader string
	var gotPath string
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotRealmHeader = r.Header.Get("X-Gitlab-Realm")
		gotModel = gjson.GetBytes(readBody(t, r), "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"cmd":"ls"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":4}}`))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"duo_gateway_base_url": srv.URL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "anthropic",
			"model_name":           "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gitlab-duo",
		Payload: []byte(`{
			"model":"gitlab-duo",
			"messages":[{"role":"user","content":[{"type":"text","text":"list files"}]}],
			"tools":[{"name":"Bash","description":"run bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}],
			"max_tokens":128
		}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/proxy/anthropic/v1/messages" {
		t.Fatalf("Path = %q, want %q", gotPath, "/v1/proxy/anthropic/v1/messages")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("Authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if gotRealmHeader != "saas" {
		t.Fatalf("X-Gitlab-Realm = %q, want saas", gotRealmHeader)
	}
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want claude-sonnet-4-5", gotModel)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.type").String(); got != "tool_use" {
		t.Fatalf("expected tool_use response, got %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.name").String(); got != "Bash" {
		t.Fatalf("expected tool name Bash, got %q", got)
	}
}

func TestGitLabExecutorExecuteUsesOpenAIGateway(t *testing.T) {
	var gotAuthHeader, gotRealmHeader string
	var gotPath string
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotRealmHeader = r.Header.Get("X-Gitlab-Realm")
		gotModel = gjson.GetBytes(readBody(t, r), "model").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"gpt-5-codex\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello from openai gateway\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"gpt-5-codex\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from openai gateway\"}]}],\"usage\":{\"input_tokens\":11,\"output_tokens\":4,\"total_tokens\":15}}}\n\n"))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"duo_gateway_base_url": srv.URL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "openai",
			"model_name":           "gpt-5-codex",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":"hello"}]}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/proxy/openai/v1/responses" {
		t.Fatalf("Path = %q, want %q", gotPath, "/v1/proxy/openai/v1/responses")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("Authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if gotRealmHeader != "saas" {
		t.Fatalf("X-Gitlab-Realm = %q, want saas", gotRealmHeader)
	}
	if gotModel != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", gotModel)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello from openai gateway" {
		t.Fatalf("expected openai gateway response, got %q payload=%s", got, string(resp.Payload))
	}
}

func TestGitLabExecutorExecuteUsesRequestedModelToSelectOpenAIGateway(t *testing.T) {
	var gotAuthHeader, gotRealmHeader, gotBetaHeader, gotUserAgent string
	var gotPath string
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotRealmHeader = r.Header.Get("X-Gitlab-Realm")
		gotBetaHeader = r.Header.Get("anthropic-beta")
		gotUserAgent = r.Header.Get("User-Agent")
		gotModel = gjson.GetBytes(readBody(t, r), "model").String()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"duo-chat-gpt-5-codex\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello from explicit openai model\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"duo-chat-gpt-5-codex\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from explicit openai model\"}]}],\"usage\":{\"input_tokens\":11,\"output_tokens\":4,\"total_tokens\":15}}}\n\n"))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"duo_gateway_base_url": srv.URL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "anthropic",
			"model_name":           "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "duo-chat-gpt-5-codex",
		Payload: []byte(`{"model":"duo-chat-gpt-5-codex","messages":[{"role":"user","content":"hello"}]}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/proxy/openai/v1/responses" {
		t.Fatalf("Path = %q, want %q", gotPath, "/v1/proxy/openai/v1/responses")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("Authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if gotRealmHeader != "saas" {
		t.Fatalf("X-Gitlab-Realm = %q, want saas", gotRealmHeader)
	}
	if gotBetaHeader != gitLabContext1MBeta {
		t.Fatalf("anthropic-beta = %q, want %q", gotBetaHeader, gitLabContext1MBeta)
	}
	if gotUserAgent != gitLabNativeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, gitLabNativeUserAgent)
	}
	if gotModel != "duo-chat-gpt-5-codex" {
		t.Fatalf("model = %q, want duo-chat-gpt-5-codex", gotModel)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello from explicit openai model" {
		t.Fatalf("expected explicit openai model response, got %q payload=%s", got, string(resp.Payload))
	}
}

func TestGitLabExecutorRefreshUpdatesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "oauth-refreshed",
				"refresh_token": "oauth-refresh",
				"token_type":    "Bearer",
				"scope":         "api read_user",
				"created_at":    1710000000,
				"expires_in":    3600,
			})
		case "/api/v4/code_suggestions/direct_access":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base_url":   "https://cloud.gitlab.example.com",
				"token":      "gateway-token",
				"expires_at": 1710003600,
				"headers":    map[string]string{"X-Gitlab-Realm": "saas"},
				"model_details": map[string]any{
					"model_provider": "anthropic",
					"model_name":     "claude-sonnet-4-5",
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "gitlab-auth.json",
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":         srv.URL,
			"access_token":     "oauth-access",
			"refresh_token":    "oauth-refresh",
			"oauth_client_id":  "client-id",
			"auth_method":      "oauth",
			"oauth_expires_at": "2000-01-01T00:00:00Z",
		},
	}

	updated, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got := updated.Metadata["access_token"]; got != "oauth-refreshed" {
		t.Fatalf("expected refreshed access token, got %#v", got)
	}
	if got := updated.Metadata["model_name"]; got != "claude-sonnet-4-5" {
		t.Fatalf("expected refreshed model metadata, got %#v", got)
	}
}

func TestGitLabExecutorExecuteStreamUsesCodeSuggestionsSSE(t *testing.T) {
	var gotAccept, gotStreamingHeader, gotEncoding string
	var gotStreamFlag bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gitLabCodeSuggestionsEndpoint {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		gotAccept = r.Header.Get("Accept")
		gotStreamingHeader = r.Header.Get(gitLabSSEStreamingHeader)
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotStreamFlag = gjson.GetBytes(readBody(t, r), "stream").Bool()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: stream_start\n"))
		_, _ = w.Write([]byte("data: {\"model\":{\"name\":\"claude-sonnet-4-5\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_chunk\n"))
		_, _ = w.Write([]byte("data: {\"content\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("event: content_chunk\n"))
		_, _ = w.Write([]byte("data: {\"content\":\" world\"}\n\n"))
		_, _ = w.Write([]byte("event: stream_end\n"))
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":     srv.URL,
			"access_token": "oauth-access",
			"model_name":   "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	lines := collectStreamLines(t, result)
	if gotAccept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotStreamingHeader != "true" {
		t.Fatalf("%s = %q, want true", gitLabSSEStreamingHeader, gotStreamingHeader)
	}
	if gotEncoding != "identity" {
		t.Fatalf("Accept-Encoding = %q, want identity", gotEncoding)
	}
	if !gotStreamFlag {
		t.Fatalf("expected upstream request to set stream=true")
	}
	if len(lines) < 4 {
		t.Fatalf("expected translated stream chunks, got %d", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), `"content":"hello"`) {
		t.Fatalf("expected hello delta in stream, got %q", strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), `"content":" world"`) {
		t.Fatalf("expected world delta in stream, got %q", strings.Join(lines, "\n"))
	}
	last := lines[len(lines)-1]
	if last != "data: [DONE]" && !strings.Contains(last, `"finish_reason":"stop"`) {
		t.Fatalf("expected stream terminator, got %q", last)
	}
}

func TestGitLabExecutorExecuteStreamFallsBackToSyntheticChat(t *testing.T) {
	chatCalls := 0
	streamCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case gitLabCodeSuggestionsEndpoint:
			streamCalls++
			http.Error(w, "feature unavailable", http.StatusForbidden)
		case gitLabChatEndpoint:
			chatCalls++
			_, _ = w.Write([]byte(`"chat fallback response"`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":     srv.URL,
			"access_token": "oauth-access",
			"model_name":   "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	lines := collectStreamLines(t, result)
	if streamCalls != 1 {
		t.Fatalf("expected streaming endpoint once, got %d", streamCalls)
	}
	if chatCalls != 1 {
		t.Fatalf("expected chat fallback once, got %d", chatCalls)
	}
	if !strings.Contains(strings.Join(lines, "\n"), `"content":"chat fallback response"`) {
		t.Fatalf("expected fallback content in stream, got %q", strings.Join(lines, "\n"))
	}
}

func TestGitLabExecutorExecuteStreamUsesAnthropicGateway(t *testing.T) {
	var gotPath, gotBetaHeader, gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBetaHeader = r.Header.Get("Anthropic-Beta")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from gateway\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":10,\"output_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"duo_gateway_base_url": srv.URL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "anthropic",
			"model_name":           "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"max_tokens":64}`),
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	lines := collectStreamLines(t, result)
	if gotPath != "/v1/proxy/anthropic/v1/messages" {
		t.Fatalf("Path = %q, want %q", gotPath, "/v1/proxy/anthropic/v1/messages")
	}
	if !strings.Contains(gotBetaHeader, gitLabContext1MBeta) {
		t.Fatalf("Anthropic-Beta = %q, want to contain %q", gotBetaHeader, gitLabContext1MBeta)
	}
	if gotUserAgent != gitLabNativeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, gitLabNativeUserAgent)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "hello from gateway") {
		t.Fatalf("expected anthropic gateway stream, got %q", strings.Join(lines, "\n"))
	}
}

func collectStreamLines(t *testing.T, result *cliproxyexecutor.StreamResult) []string {
	t.Helper()
	lines := make([]string, 0, 8)
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		lines = append(lines, string(chunk.Payload))
	}
	return lines
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return body
}
