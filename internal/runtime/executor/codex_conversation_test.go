package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexUsesConversationAPIForOAuthAuthFile(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "oauth-token",
		},
	}

	if !codexUsesConversationAPI(auth) {
		t.Fatal("expected OAuth auth-file credentials to use conversation bridge")
	}

	auth.Attributes = map[string]string{"api_key": "sk-test"}
	if codexUsesConversationAPI(auth) {
		t.Fatal("did not expect codex-api-key credentials to use conversation bridge")
	}
}

func TestBuildCodexConversationPromptBridgesTranscript(t *testing.T) {
	body := []byte(`{
		"instructions":"Follow the repository conventions.",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"solve this bug"}]}
		]
	}`)

	prompt, err := buildCodexConversationPrompt(body)
	if err != nil {
		t.Fatalf("buildCodexConversationPrompt() error = %v", err)
	}

	for _, want := range []string{
		"System:\nFollow the repository conventions.",
		"User:\nhello",
		"Assistant:\nhi",
		"User:\nsolve this bug",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestBuildCodexConversationPromptRejectsTools(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"lookup"}],
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]
	}`)

	_, err := buildCodexConversationPrompt(body)
	if err == nil {
		t.Fatal("expected tool request to be rejected")
	}
	if status, ok := err.(statusErr); !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("err = %T %v, want statusErr 400", err, err)
	}
}

func TestBuildCodexConversationRequestIncludesBrowserFields(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5",
		"input":"hello",
		"conversation_id":"conv-123",
		"parent_message_id":"parent-123",
		"history_and_training_disabled":true
	}`)

	raw, err := buildCodexConversationRequest(body, nil)
	if err != nil {
		t.Fatalf("buildCodexConversationRequest() error = %v", err)
	}

	if got := gjson.GetBytes(raw, "conversation_id").String(); got != "conv-123" {
		t.Fatalf("conversation_id = %q, want %q", got, "conv-123")
	}
	if got := gjson.GetBytes(raw, "parent_message_id").String(); got != "parent-123" {
		t.Fatalf("parent_message_id = %q, want %q", got, "parent-123")
	}
	if got := gjson.GetBytes(raw, "conversation_mode.kind").String(); got != "primary_assistant" {
		t.Fatalf("conversation_mode.kind = %q, want %q", got, "primary_assistant")
	}
	if !gjson.GetBytes(raw, "supports_buffering").Bool() {
		t.Fatal("supports_buffering = false, want true")
	}
	if !gjson.GetBytes(raw, "suggestions").IsArray() {
		t.Fatal("suggestions should be an array")
	}
	if got := gjson.GetBytes(raw, "messages.0.metadata").Raw; got != `{}` {
		t.Fatalf("message metadata = %s, want {}", got)
	}
	if got := gjson.GetBytes(raw, "timezone_offset_min").Int(); got != -480 {
		t.Fatalf("timezone_offset_min = %d, want %d", got, -480)
	}
	if got := gjson.GetBytes(raw, "timezone").String(); got != "Asia/Shanghai" {
		t.Fatalf("timezone = %q, want %q", got, "Asia/Shanghai")
	}
	if !gjson.GetBytes(raw, "history_and_training_disabled").Bool() {
		t.Fatal("history_and_training_disabled = false, want true")
	}
}

func TestCodexExecutorExecuteStreamConversationOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/csrf":
			w.Header().Add("Set-Cookie", "__Host-next-auth.csrf-token=csrf-value; Path=/")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"csrfToken":"csrf-value"}`)
			return
		case "/backend-api/me":
			if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
				t.Fatalf("validate Authorization = %q, want %q", got, "Bearer oauth-token")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"user-1"}`)
			return
		case "/backend-api/sentinel/chat-requirements":
			if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
				t.Fatalf("requirements Authorization = %q, want %q", got, "Bearer oauth-token")
			}
			if got := r.Header.Get("Oai-Device-Id"); got == "" {
				t.Fatal("requirements missing Oai-Device-Id")
			}
			if got := r.Header.Get("Cookie"); !strings.Contains(got, "oai-did=") {
				t.Fatalf("requirements Cookie = %q, want oai-did", got)
			}
			w.Header().Add("Set-Cookie", "oai-sc=oai-sc-value; Path=/")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"req-token","proofofwork":{"required":false}}`)
			return
		case "/backend-api/conversation":
		default:
			t.Fatalf("path = %q, want sentinel or conversation endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer oauth-token")
		}
		if got := r.Header.Get("Openai-Sentinel-Chat-Requirements-Token"); got != "req-token" {
			t.Fatalf("requirements token = %q, want %q", got, "req-token")
		}
		if got := r.Header.Get("Openai-Sentinel-Proof-Token"); got == "" || !strings.HasPrefix(got, "gAAAAAC") {
			t.Fatalf("proof token = %q, want gAAAAAC...", got)
		}
		if got := r.Header.Get("Cookie"); !strings.Contains(got, "oai-did=") || !strings.Contains(got, "oai-sc=oai-sc-value") || !strings.Contains(got, "__Secure-next-auth.session-token=session-123") {
			t.Fatalf("Cookie = %q, want oai-did + oai-sc + session token", got)
		}

		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if got := gjson.GetBytes(reqBody, "model").String(); got != "gpt-5" {
			t.Fatalf("model = %q, want %q", got, "gpt-5")
		}
		if got := gjson.GetBytes(reqBody, "messages.0.content.parts.0").String(); got != "hello" {
			t.Fatalf("prompt = %q, want %q", got, "hello")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg-1\",\"author\":{\"role\":\"assistant\"},\"content\":{\"content_type\":\"text\",\"parts\":[\"Hel\"]},\"status\":\"in_progress\",\"metadata\":{\"model_slug\":\"gpt-5\"}},\"conversation_id\":\"conv-1\",\"error\":null}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg-1\",\"author\":{\"role\":\"assistant\"},\"content\":{\"content_type\":\"text\",\"parts\":[\"Hello\"]},\"status\":\"finished_successfully\",\"metadata\":{\"model_slug\":\"gpt-5\"}},\"conversation_id\":\"conv-1\",\"error\":null}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	exec := NewCodexExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL + "/backend-api/conversation",
		},
		Metadata: map[string]any{
			"access_token":  "oauth-token",
			"account_id":    "acct-1",
			"session_token": "session-123",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: []byte(`{"model":"gpt-5","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var payloads []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payloads = append(payloads, string(chunk.Payload))
	}

	joined := strings.Join(payloads, "\n")
	for _, want := range []string{
		`"type":"response.created"`,
		`"type":"response.output_text.delta"`,
		`"item_id":"msg-1"`,
		`"delta":"Hel"`,
		`"delta":"lo"`,
		`"type":"response.completed"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream output missing %q:\n%s", want, joined)
		}
	}
}

func TestCodexConversationStreamStateConsumesAppendOperations(t *testing.T) {
	state := newCodexConversationStreamState("gpt-5")

	firstEvents, completed, err := state.consumePayload([]byte(`{"conversation_id":"conv-ops","v":[{"o":"append","p":"/message/content/parts/0","v":"Hel","message_id":"msg-ops"}]}`))
	if err != nil {
		t.Fatalf("first consumePayload() error = %v", err)
	}
	if completed {
		t.Fatal("first append event should not complete stream")
	}
	firstJoined := joinCodexConversationEvents(firstEvents)
	for _, want := range []string{`"type":"response.created"`, `"delta":"Hel"`, `"item_id":"msg-ops"`} {
		if !strings.Contains(firstJoined, want) {
			t.Fatalf("first event output missing %q:\n%s", want, firstJoined)
		}
	}

	secondEvents, completed, err := state.consumePayload([]byte(`{"conversation_id":"conv-ops","v":[{"o":"append","p":"/message/content/parts/0","v":"lo","message_id":"msg-ops"}]}`))
	if err != nil {
		t.Fatalf("second consumePayload() error = %v", err)
	}
	if completed {
		t.Fatal("second append event should not complete stream")
	}
	secondJoined := joinCodexConversationEvents(secondEvents)
	if !strings.Contains(secondJoined, `"delta":"lo"`) {
		t.Fatalf("second event output missing append delta:\n%s", secondJoined)
	}

	doneEvents, completed, err := state.consumePayload([]byte(`[DONE]`))
	if err != nil {
		t.Fatalf("done consumePayload() error = %v", err)
	}
	if !completed {
		t.Fatal("DONE should complete stream")
	}
	doneJoined := joinCodexConversationEvents(doneEvents)
	if !strings.Contains(doneJoined, `"type":"response.completed"`) || !strings.Contains(doneJoined, `"text":"Hello"`) {
		t.Fatalf("done output missing completion payload:\n%s", doneJoined)
	}
}

func joinCodexConversationEvents(events [][]byte) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, string(event))
	}
	return strings.Join(parts, "\n")
}

func TestCodexExecutorExecuteCompactConversationOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/csrf":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"csrfToken":"csrf-value"}`)
			return
		case "/backend-api/me":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"user-1"}`)
			return
		case "/backend-api/sentinel/chat-requirements":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"token":"req-token","proofofwork":{"required":false}}`)
			return
		case "/backend-api/conversation":
		default:
			t.Fatalf("path = %q, want %q or sentinel", r.URL.Path, "/backend-api/conversation")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg-2\",\"author\":{\"role\":\"assistant\"},\"content\":{\"content_type\":\"text\",\"parts\":[\"Hello world\"]},\"status\":\"finished_successfully\",\"metadata\":{\"model_slug\":\"gpt-5\"}},\"conversation_id\":\"conv-2\",\"error\":null}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	exec := NewCodexExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL + "/backend-api/conversation",
		},
		Metadata: map[string]any{
			"access_token": "oauth-token",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: []byte(`{"model":"gpt-5","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		Alt:          "responses/compact",
		SourceFormat: sdktranslator.FromString("openai-response"),
	}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "gpt-5" {
		t.Fatalf("model = %q, want %q, payload=%s", got, "gpt-5", string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "Hello world" {
		t.Fatalf("text = %q, want %q, payload=%s", got, "Hello world", string(resp.Payload))
	}
}

func TestResolveCodexConversationBearerTokenRefreshesBySessionToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/csrf":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"csrfToken":"csrf-value"}`)
			return
		case "/backend-api/me":
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
			return
		case "/api/auth/session":
			if got := r.Header.Get("Cookie"); !strings.Contains(got, "__Secure-next-auth.session-token=session-xyz") {
				t.Fatalf("session refresh Cookie = %q, want session token", got)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"accessToken":"fresh-token"}`)
			return
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	exec := NewCodexExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL + "/backend-api/conversation",
		},
		Metadata: map[string]any{
			"access_token":  "stale-token",
			"session_token": "session-xyz",
		},
	}

	token, err := exec.resolveCodexConversationBearerToken(context.Background(), auth, server.URL+"/backend-api/conversation")
	if err != nil {
		t.Fatalf("resolveCodexConversationBearerToken() error = %v", err)
	}
	if token != "fresh-token" {
		t.Fatalf("token = %q, want %q", token, "fresh-token")
	}
	if got := metaStringValue(auth.Metadata, "access_token"); got != "fresh-token" {
		t.Fatalf("auth.Metadata[access_token] = %q, want %q", got, "fresh-token")
	}
}

func TestResolveCodexConversationBearerTokenDoesNotReuseStaleToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/csrf":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"csrfToken":"csrf-value"}`)
			return
		case "/backend-api/me":
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
			return
		case "/api/auth/session":
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
			return
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	exec := NewCodexExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL + "/backend-api/conversation",
		},
		Metadata: map[string]any{
			"access_token":  "stale-token",
			"session_token": "session-xyz",
		},
	}

	token, err := exec.resolveCodexConversationBearerToken(context.Background(), auth, server.URL+"/backend-api/conversation")
	if err == nil {
		t.Fatalf("resolveCodexConversationBearerToken() token = %q, want error", token)
	}
}
