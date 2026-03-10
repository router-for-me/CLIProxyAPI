package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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
			"base_url":            srv.URL,
			"access_token":        "oauth-access",
			"refresh_token":       "oauth-refresh",
			"oauth_client_id":     "client-id",
			"oauth_client_secret": "client-secret",
			"auth_method":         "oauth",
			"oauth_expires_at":    "2000-01-01T00:00:00Z",
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
	if last := lines[len(lines)-1]; last != "data: [DONE]" {
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
