package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	clipoauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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

func TestOpenCodeCredentialsFromMetadata(t *testing.T) {
	auth := &clipoauth.Auth{Metadata: map[string]any{"api_key": "sk-test"}}
	token, ok := openCodeCreds(auth)
	if !ok || token != "sk-test" {
		t.Fatalf("openCodeCreds = (%q, %v), want (sk-test, true)", token, ok)
	}
}

func TestOpenCodeExecutor_NilAuthClone(t *testing.T) {
	exec := NewOpenCodeExecutor(constant.OpenCode)
	cloned := exec.cloneAuthWithBaseURL(nil)
	if cloned != nil {
		t.Fatalf("expected nil, got %v", cloned)
	}
}

// openCodeSessionServer answers both the chat and messages paths and records
// the X-Opencode-Session header each upstream request carried.
func openCodeSessionServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	seen := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Header.Get("X-Opencode-Session"))
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			for _, frame := range []string{
				"event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":"end_turn","usage":{"output_tokens":5,"input_tokens":5}}}`,
				"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`,
				"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
				"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}`,
				"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"input_tokens":5}}`,
				"event: message_stop\n" + `data: {"type":"message_stop"}`,
				"data: [DONE]",
			} {
				_, _ = w.Write([]byte(frame + "\n\n"))
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"c-1","object":"chat.completion","choices":[{"index":0,"message":{"content":"OK","role":"assistant"}}]}`))
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func TestOpenCodeExecutor_SendsConversationSession(t *testing.T) {
	derived := map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:probe"}
	send := func(t *testing.T, model string, metadata map[string]any, headers http.Header) string {
		t.Helper()
		server, seen := openCodeSessionServer(t)
		exec := NewOpenCodeExecutor(constant.OpenCodeGo)
		exec.cfg = &config.Config{}
		auth := &clipoauth.Auth{
			Provider:   constant.OpenCodeGo,
			Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
		}
		payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Respond with OK."}]}`)
		req := cliproxyexecutor.Request{Model: model, Payload: payload, Metadata: metadata}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: headers, OriginalRequest: payload, Metadata: metadata}
		if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if len(*seen) != 1 {
			t.Fatalf("upstream requests = %d, want 1", len(*seen))
		}
		if _, leaked := auth.Attributes["header:X-Opencode-Session"]; leaked {
			t.Fatal("request session leaked into the shared auth attributes")
		}
		if _, err := uuid.Parse((*seen)[0]); err != nil {
			t.Fatalf("upstream X-Opencode-Session = %q, want a UUID: %v", (*seen)[0], err)
		}
		return (*seen)[0]
	}

	chat := send(t, "deepseek-v4-pro", derived, nil)
	if messages := send(t, "claude-opus-5", derived, nil); messages != chat {
		t.Fatalf("one conversation sent two sessions: chat %q, messages %q", chat, messages)
	}
	if other := send(t, "deepseek-v4-pro", map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:other"}, nil); other == chat {
		t.Fatalf("two conversations shared session %q", chat)
	}
	// A request with no declared or derived identity still carries a session:
	// the gateway rejects one without it.
	send(t, "deepseek-v4-pro", nil, nil)

	// A session the client declared selects the upstream session, whatever
	// header it used and whatever characters it holds, and never leaves verbatim.
	for _, headers := range []http.Header{
		{"X-Opencode-Session": {"client-session-1"}},
		{"X-Session-Id": {"agent-session-2"}},
		{"X-Opencode-Session": {"$X-Session-Id"}},
	} {
		declared := send(t, "deepseek-v4-pro", derived, headers)
		if declared == chat {
			t.Fatalf("declared session %v did not select the upstream session", headers)
		}
		if again := send(t, "deepseek-v4-pro", nil, headers); again != declared {
			t.Fatalf("declared session %v is not stable: %q then %q", headers, declared, again)
		}
	}
}

// An OpenCode credential may keep its key in metadata; the delegated messages
// executor must still authenticate with it instead of calling anonymously.
func TestOpenCodeExecutor_DelegatedMessagesSendMetadataKey(t *testing.T) {
	var gotAuth, gotXAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXAPIKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, frame := range []string{
			"event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":"end_turn","usage":{"output_tokens":1,"input_tokens":1}}}`,
			"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}`,
			"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1,"input_tokens":1}}`,
			"event: message_stop\n" + `data: {"type":"message_stop"}`,
		} {
			_, _ = w.Write([]byte(frame + "\n\n"))
		}
	}))
	t.Cleanup(server.Close)

	exec := NewOpenCodeExecutor(constant.OpenCode)
	exec.cfg = &config.Config{}
	auth := &clipoauth.Auth{
		Provider:   constant.OpenCode,
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"api_key": "metadata-key"},
	}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"Respond with OK."}]}`)
	req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload}
	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotXAPIKey != "metadata-key" || gotAuth != "" {
		t.Fatalf("delegated request authentication = (Authorization %q, X-Api-Key %q), want x-api-key with the metadata key", gotAuth, gotXAPIKey)
	}
	if _, leaked := auth.Attributes["api_key"]; leaked {
		t.Fatal("request key leaked into the shared auth attributes")
	}
}
