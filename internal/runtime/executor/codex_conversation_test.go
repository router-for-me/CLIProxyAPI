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

func TestCodexExecutorExecuteStreamConversationOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/backend-api/conversation")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer oauth-token")
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
			"access_token": "oauth-token",
			"account_id":   "acct-1",
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

func TestCodexExecutorExecuteCompactConversationOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/backend-api/conversation")
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
