package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	clipoauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenCodeExecutor_RoutesByProtocol(t *testing.T) {
	cases := []struct {
		name       string
		gateway    string
		modelID    string
		wantPath   string
		wantFormat sdktranslator.Format
		wantErr    bool
	}{
		{"zen-responses", "opencode", "gpt-5.6-luna", "/v1/responses", sdktranslator.FormatOpenAIResponse, false},
		{"zen-messages", "opencode", "claude-opus-5", "/v1/messages", sdktranslator.FormatClaude, false},
		{"zen-chat", "opencode", "deepseek-v4-pro", "/v1/chat/completions", sdktranslator.FormatOpenAI, false},
		{"go-responses", "opencode-go", "gpt-5.6-luna", "/v1/responses", sdktranslator.FormatOpenAIResponse, false},
		{"go-messages", "opencode-go", "claude-opus-5", "/v1/messages", sdktranslator.FormatClaude, false},
		{"go-chat", "opencode-go", "deepseek-v4-pro", "/v1/chat/completions", sdktranslator.FormatOpenAI, false},
		{"unknown-model", "opencode", "no-such-model", "", sdktranslator.FormatOpenAI, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := NewOpenCodeExecutor(tc.gateway)
			if exec.Identifier() != tc.gateway {
				t.Fatalf("Identifier() = %q, want %q", exec.Identifier(), tc.gateway)
			}
			if tc.wantErr {
				_, _, err := exec.resolveRoute(tc.modelID)
				if err == nil {
					t.Fatalf("expected error for unknown model, got nil")
				}
				return
			}
			proto, path, err := exec.resolveRoute(tc.modelID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if proto == "" {
				t.Fatal("expected non-empty protocol")
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			gotFormat := exec.RequestToFormat(cliproxyexecutor.Request{Model: tc.modelID}, cliproxyexecutor.Options{})
			if gotFormat != tc.wantFormat {
				t.Errorf("format = %q, want %q", gotFormat, tc.wantFormat)
			}
		})
	}
}

func TestOpenCodeExecutor_ExecuteDispatchesResponses(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"resp-1","object":"chat.completion","choices":[{"index":0,"message":{"content":"hi","role":"assistant"}}]}`))
	}))
	defer server.Close()

	exec := NewOpenCodeExecutor(constant.OpenCode)
	exec.cfg = &config.Config{}

	auth := &clipoauth.Auth{
		Provider:   constant.OpenCode,
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", capturedPath)
	}
	if len(resp.Payload) == 0 {
		t.Error("expected non-empty response payload")
	}
}

func TestOpenCodeExecutor_ExecuteDispatchesMessages(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":"end_turn","usage":{"output_tokens":5,"input_tokens":5}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_stop","index":0}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"input_tokens":5}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewOpenCodeExecutor(constant.OpenCode)
	exec.cfg = &config.Config{}

	auth := &clipoauth.Auth{
		Provider:   constant.OpenCode,
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}
	req := cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", capturedPath)
	}
	if len(resp.Payload) == 0 {
		t.Error("expected non-empty response payload")
	}
}

func TestOpenCodeExecutor_ExecuteUnknownModelReturnsError(t *testing.T) {
	exec := NewOpenCodeExecutor(constant.OpenCode)
	exec.cfg = &config.Config{}

	auth := &clipoauth.Auth{Provider: constant.OpenCode, Metadata: map[string]any{"api_key": "k"}}
	req := cliproxyexecutor.Request{Model: "nonexistent-model-x9"}

	_, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error = %v, want 'not available'", err)
	}
}

func TestOpenCodeExecutor_CloneAuthWithBaseURL(t *testing.T) {
	exec := NewOpenCodeExecutor(constant.OpenCode)
	auth := &clipoauth.Auth{
		Provider:   constant.OpenCode,
		Attributes: map[string]string{},
		Metadata:   map[string]any{"api_key": "token"},
	}
	cloned := exec.cloneAuthWithBaseURL(auth)
	if cloned == nil {
		t.Fatal("expected non-nil cloned auth")
	}
	if cloned.Attributes["base_url"] != OpenCodeZenBaseURL {
		t.Errorf("base_url = %q, want %q", cloned.Attributes["base_url"], OpenCodeZenBaseURL)
	}
	if len(auth.Attributes) != 0 {
		t.Error("original auth Attributes was mutated")
	}
}

func TestPoolsideExecutor_ExecuteDispatchesMessages(t *testing.T) {
	var capturedPath, authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":"end_turn","usage":{"output_tokens":5,"input_tokens":5}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_stop","index":0}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"input_tokens":5}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	exec := NewPoolsideExecutor(&config.Config{})
	auth := &clipoauth.Auth{
		Provider:   constant.Poolside,
		Attributes: map[string]string{"api_key": "poolside-secret", "base_url": server.URL},
	}
	req := cliproxyexecutor.Request{
		Model:   "claude-opus-4-8",
		Payload: []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", capturedPath)
	}
	if authHeader != "Bearer poolside-secret" {
		t.Errorf("Authorization = %q, want Bearer poolside-secret", authHeader)
	}
	if len(resp.Payload) == 0 {
		t.Error("expected non-empty response payload")
	}
}

func TestPoolsideExecutor_IdentifierAndProvider(t *testing.T) {
	exec := NewPoolsideExecutor(&config.Config{})
	if exec.Identifier() != constant.Poolside {
		t.Errorf("Identifier() = %q, want %q", exec.Identifier(), constant.Poolside)
	}
	if exec.ProviderKey() != constant.Poolside {
		t.Errorf("ProviderKey() = %q, want %q", exec.ProviderKey(), constant.Poolside)
	}
}

func TestOpenCodeCredentialsFromMetadata(t *testing.T) {
	auth := &clipoauth.Auth{Metadata: map[string]any{"api_key": "sk-test"}}
	token, ok := openCodeCreds(auth)
	if !ok || token != "sk-test" {
		t.Fatalf("openCodeCreds = (%q, %v), want (sk-test, true)", token, ok)
	}
}

func TestPoolsideCredentialsFromMetadata(t *testing.T) {
	auth := &clipoauth.Auth{Metadata: map[string]any{"api_key": "sp-test"}}
	token, ok := poolsideCreds(auth)
	if !ok || token != "sp-test" {
		t.Fatalf("poolsideCreds = (%q, %v), want (sp-test, true)", token, ok)
	}
}

func TestOpenCodeExecutor_NilAuthClone(t *testing.T) {
	exec := NewOpenCodeExecutor(constant.OpenCode)
	cloned := exec.cloneAuthWithBaseURL(nil)
	if cloned != nil {
		t.Fatalf("expected nil, got %v", cloned)
	}
}
