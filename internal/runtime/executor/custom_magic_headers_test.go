package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
