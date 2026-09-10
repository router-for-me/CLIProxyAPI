package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCustomMagicHeaders_OpenAICompat(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "test-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Forwarded-Session":      "$X-Client-Session",
			"header:X-Missing":                "$NONEXISTENT",
			"header:X-Static":                 "static-value",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"Abc":              []string{"session-abc-value"},
			"X-Client-Session": []string{"client-session-uuid-123"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "session-abc-value" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "session-abc-value")
	}
	if got := gotHeaders.Get("X-Forwarded-Session"); got != "client-session-uuid-123" {
		t.Errorf("X-Forwarded-Session = %q, want %q", got, "client-session-uuid-123")
	}
	if got := gotHeaders.Get("X-Static"); got != "static-value" {
		t.Errorf("X-Static = %q, want %q", got, "static-value")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_Gemini(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "gemini-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Missing":                "$NONEXISTENT",
			"header:X-Static":                 "gemini-static",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
		Headers: http.Header{
			"Abc": []string{"gemini-session-abc"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "gemini-session-abc" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "gemini-session-abc")
	}
	if got := gotHeaders.Get("X-Static"); got != "gemini-static" {
		t.Errorf("X-Static = %q, want %q", got, "gemini-static")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_GeminiInteractions(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","outputs":[{"text":"ok"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "interactions-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Missing":                "$NONEXISTENT",
			"header:X-Static":                 "interactions-static",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"Abc": []string{"interactions-session-123"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "interactions-session-123" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "interactions-session-123")
	}
	if got := gotHeaders.Get("X-Static"); got != "interactions-static" {
		t.Errorf("X-Static = %q, want %q", got, "interactions-static")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_GeminiVertex(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"vertex-response"}]}}]}`))
	}))
	defer server.Close()

	executor := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "vertex-api-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Missing":                "$NONEXISTENT",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
		Headers: http.Header{
			"Abc": []string{"vertex-session-123"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "vertex-session-123" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "vertex-session-123")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_XAI(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null,\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "xai-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Missing":                "$NONEXISTENT",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "grok-2",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"ABC": []string{"xai-session-value"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "xai-session-value" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "xai-session-value")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_Claude(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "sk-ant-test",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Missing":                "$NONEXISTENT",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "claude-3-7-sonnet-20250219",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"Abc": []string{"claude-session-value"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "claude-session-value" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "claude-session-value")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_OpenAICompat_Stream(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "test-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Empty-Var":              "$   ",
			"header:X-Only-Dollar":            "$",
			"header:X-Missing":                "$NONEXISTENT",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
		Headers: http.Header{
			"Abc": []string{"stream-session-abc"},
		},
	}

	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range result.Chunks {
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "stream-session-abc" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "stream-session-abc")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
	if _, exists := gotHeaders["X-Empty-Var"]; exists {
		t.Errorf("expected X-Empty-Var to be omitted, got %q", gotHeaders.Get("X-Empty-Var"))
	}
	if _, exists := gotHeaders["X-Only-Dollar"]; exists {
		t.Errorf("expected X-Only-Dollar to be omitted, got %q", gotHeaders.Get("X-Only-Dollar"))
	}
}

func TestCustomMagicHeaders_Codex(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null,\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: true,
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "codex-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Missing":                "$NONEXISTENT",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatCodex,
		Headers: http.Header{
			"Abc": []string{"codex-session-value"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "codex-session-value" {
		t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "codex-session-value")
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted, got %q", gotHeaders.Get("X-Missing"))
	}
}

func TestCustomMagicHeaders_CPASessionID_OpenAICompat(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":             server.URL,
			"api_key":              "test-key",
			"header:X-Session":     "$CPA-SESSION-ID",
			"header:Authorization": "Bearer $CPA-SESSION-ID",
			"header:X-Lower-Case":  "$cpa-session-id",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"Session-Id": []string{"codex-thread-abc"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantSession := "codex:codex-thread-abc"
	if got := gotHeaders.Get("X-Session"); got != wantSession {
		t.Errorf("X-Session = %q, want %q", got, wantSession)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer "+wantSession {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+wantSession)
	}
	if got := gotHeaders.Get("X-Lower-Case"); got != wantSession {
		t.Errorf("X-Lower-Case = %q, want %q", got, wantSession)
	}
}

func TestCustomMagicHeaders_CPASessionID_Gemini(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"base_url":         server.URL,
			"api_key":          "gemini-key",
			"header:X-Session": "$CPA-SESSION-ID",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
		Headers: http.Header{
			"X-Session-ID": []string{"gemini-session-123"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantSession := "header:gemini-session-123"
	if got := gotHeaders.Get("X-Session"); got != wantSession {
		t.Errorf("X-Session = %q, want %q", got, wantSession)
	}
}

func TestCustomMagicHeaders_CPASessionID_GeminiInteractions(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","status":"completed","outputs":[{"text":"ok"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"base_url":         server.URL,
			"api_key":          "interactions-key",
			"header:X-Session": "$CPA-SESSION-ID",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-flash-lite",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"X-Session-Affinity": []string{"interactions-sess-456"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantSession := "affinity:interactions-sess-456"
	if got := gotHeaders.Get("X-Session"); got != wantSession {
		t.Errorf("X-Session = %q, want %q", got, wantSession)
	}
}

func TestCustomMagicHeaders_CPASessionID_Claude(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"base_url":         server.URL,
			"api_key":          "sk-ant-test",
			"header:X-Session": "$CPA-SESSION-ID",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "claude-3-7-sonnet-20250219",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers: http.Header{
			"X-Claude-Code-Session-Id": []string{"claude-sess-789"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantSession := "claude:claude-sess-789"
	if got := gotHeaders.Get("X-Session"); got != wantSession {
		t.Errorf("X-Session = %q, want %q", got, wantSession)
	}
}

func TestCustomMagicHeaders_CPASessionID_Codex(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null,\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: true,
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":         server.URL,
			"api_key":          "codex-key",
			"header:X-Session": "$CPA-SESSION-ID",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatCodex,
		Headers: http.Header{
			"Session-Id": []string{"codex-sess-uuid"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantSession := "codex:codex-sess-uuid"
	if got := gotHeaders.Get("X-Session"); got != wantSession {
		t.Errorf("X-Session = %q, want %q", got, wantSession)
	}
}

func TestCustomMagicHeaders_CPASessionID_XAI(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null,\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":         server.URL,
			"api_key":          "xai-key",
			"header:X-Session": "$CPA-SESSION-ID",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "grok-2",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"X-Session-ID": []string{"xai-sess-111"},
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantSession := "header:xai-sess-111"
	if got := gotHeaders.Get("X-Session"); got != wantSession {
		t.Errorf("X-Session = %q, want %q", got, wantSession)
	}
}

func TestCustomMagicHeaders_CPASessionID_SessionAffinityOnAndOff(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	for _, sessionAffinity := range []bool{true, false} {
		t.Run(map[bool]string{true: "SessionAffinity=true", false: "SessionAffinity=false"}[sessionAffinity], func(t *testing.T) {
			gotHeaders = nil
			cfg := &config.Config{
				Routing: config.RoutingConfig{
					SessionAffinity: sessionAffinity,
				},
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name: "compat",
					Headers: map[string]string{
						"X-CPA-Session": "$CPA-SESSION-ID",
					},
				}},
			}

			executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
			auth := &cliproxyauth.Auth{
				ID:       "auth-compat-1",
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":             server.URL,
					"api_key":              "key-1",
					"header:X-CPA-Session": "$CPA-SESSION-ID",
				},
			}

			req := cliproxyexecutor.Request{
				Model:   "gpt-4o",
				Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAI,
				Headers: http.Header{
					"X-Session-ID": []string{"aff-test-sess-99"},
				},
			}

			_, err := executor.Execute(context.Background(), auth, req, opts)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			wantSession := "header:aff-test-sess-99"
			if got := gotHeaders.Get("X-CPA-Session"); got != wantSession {
				t.Errorf("X-CPA-Session = %q, want %q (sessionAffinity=%v)", got, wantSession, sessionAffinity)
			}
		})
	}
}

func TestCustomMagicHeaders_CPASessionID_AIStudio(t *testing.T) {
	const authID = "aistudio-cpa-auth"
	connected := make(chan struct{})
	var connectedOnce sync.Once
	relay := wsrelay.NewManager(wsrelay.Options{
		ProviderFactory: func(*http.Request) (string, error) {
			return authID, nil
		},
		OnConnected: func(provider string) {
			if provider == authID {
				connectedOnce.Do(func() {
					close(connected)
				})
			}
		},
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Logf("relay stop error = %v", errStop)
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + relay.Path()
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Logf("websocket close error = %v", errClose)
		}
	}()

	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for relay connection")
	}

	receivedHeaders := make(chan http.Header, 10)
	go func() {
		for {
			var msg wsrelay.Message
			if errRead := conn.ReadJSON(&msg); errRead != nil {
				return
			}
			if msg.Type == wsrelay.MessageTypeHTTPReq && msg.Payload != nil {
				if rawHeaders, ok := msg.Payload["headers"].(map[string]any); ok {
					h := make(http.Header)
					for k, v := range rawHeaders {
						switch vals := v.(type) {
						case []any:
							for _, item := range vals {
								if str, ok := item.(string); ok {
									h.Add(k, str)
								}
							}
						case []string:
							for _, item := range vals {
								h.Add(k, item)
							}
						case string:
							h.Set(k, vals)
						}
					}
					receivedHeaders <- h
				}
				resp := wsrelay.Message{
					ID:   msg.ID,
					Type: wsrelay.MessageTypeHTTPResp,
					Payload: map[string]any{
						"status":  float64(http.StatusOK),
						"headers": map[string]any{"Content-Type": "application/json"},
						"body":    `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
					},
				}
				if errWrite := conn.WriteJSON(resp); errWrite != nil {
					return
				}
			}
		}
	}()

	executor := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	auth := &cliproxyauth.Auth{
		ID:       authID,
		Provider: "aistudio",
		Attributes: map[string]string{
			"header:X-Session":     "$CPA-SESSION-ID",
			"header:Authorization": "Bearer $CPA-SESSION-ID",
			"header:X-Lower-Case":  "$cpa-session-id",
			"header:X-Custom-Dyn":  "$X-Client-Dyn",
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
		Headers: http.Header{
			"X-Session-ID": []string{"aistudio-sess-456"},
			"X-Client-Dyn": []string{"dyn-val-789"},
		},
	}

	t.Run("Execute", func(t *testing.T) {
		_, errExecute := executor.Execute(context.Background(), auth, req, opts)
		if errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		select {
		case gotHeaders := <-receivedHeaders:
			wantSession := "header:aistudio-sess-456"
			if got := gotHeaders.Get("X-Session"); got != wantSession {
				t.Errorf("X-Session = %q, want %q", got, wantSession)
			}
			if got := gotHeaders.Get("Authorization"); got != "Bearer "+wantSession {
				t.Errorf("Authorization = %q, want %q", got, "Bearer "+wantSession)
			}
			if got := gotHeaders.Get("X-Lower-Case"); got != wantSession {
				t.Errorf("X-Lower-Case = %q, want %q", got, wantSession)
			}
			if got := gotHeaders.Get("X-Custom-Dyn"); got != "dyn-val-789" {
				t.Errorf("X-Custom-Dyn = %q, want %q", got, "dyn-val-789")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Execute headers")
		}
	})

	t.Run("ExecuteStream", func(t *testing.T) {
		streamResult, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for range streamResult.Chunks {
		}
		select {
		case gotHeaders := <-receivedHeaders:
			wantSession := "header:aistudio-sess-456"
			if got := gotHeaders.Get("X-Session"); got != wantSession {
				t.Errorf("X-Session = %q, want %q", got, wantSession)
			}
			if got := gotHeaders.Get("Authorization"); got != "Bearer "+wantSession {
				t.Errorf("Authorization = %q, want %q", got, "Bearer "+wantSession)
			}
			if got := gotHeaders.Get("X-Lower-Case"); got != wantSession {
				t.Errorf("X-Lower-Case = %q, want %q", got, wantSession)
			}
			if got := gotHeaders.Get("X-Custom-Dyn"); got != "dyn-val-789" {
				t.Errorf("X-Custom-Dyn = %q, want %q", got, "dyn-val-789")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for ExecuteStream headers")
		}
	})
}
