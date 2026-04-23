package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func newCodexDeltaOnlyTextServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1775540000,\"model\":\"gpt-5.3-codex\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.added\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_resp_1_0\",\"type\":\"message\",\"status\":\"in_progress\",\"content\":[],\"role\":\"assistant\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.content_part.added\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.content_part.added\",\"item_id\":\"msg_resp_1_0\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_resp_1_0\",\"output_index\":0,\"content_index\":0,\"delta\":\"Hello from delta\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1775540000,\"model\":\"gpt-5.3-codex\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n")
	}))
}

func newCodexTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": baseURL,
			"api_key":  "test",
		},
	}
}

func TestCodexExecutorExecute_NonStreamResponsesPreservesDeltaOnlyText(t *testing.T) {
	upstream := newCodexDeltaOnlyTextServer()
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(context.Background(), newCodexTestAuth(upstream.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "Hello from delta" {
		t.Fatalf("output text = %q, want %q; payload=%s", got, "Hello from delta", string(resp.Payload))
	}
}

func TestCodexExecutorExecute_NonStreamChatCompletionsPreservesDeltaOnlyText(t *testing.T) {
	upstream := newCodexDeltaOnlyTextServer()
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(context.Background(), newCodexTestAuth(upstream.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "Hello from delta" {
		t.Fatalf("message content = %q, want %q; payload=%s", got, "Hello from delta", string(resp.Payload))
	}
}

func TestCodexExecutorExecute_NonStreamClaudePreservesDeltaOnlyText(t *testing.T) {
	upstream := newCodexDeltaOnlyTextServer()
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(context.Background(), newCodexTestAuth(upstream.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "Hello from delta" {
		t.Fatalf("claude text = %q, want %q; payload=%s", got, "Hello from delta", string(resp.Payload))
	}
}
