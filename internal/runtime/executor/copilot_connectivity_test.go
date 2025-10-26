package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// Helper to build a Copilot auth pointing at the supplied upstream.
func newTestCopilotAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "copilot:test",
		Provider: "copilot",
		Attributes: map[string]string{
			"base_url": baseURL,
		},
		Metadata: map[string]any{
			"access_token": "atk",
		},
		Status: cliproxyauth.StatusActive,
	}
}

// emitCopilotSSE writes a minimal sequence of Copilot SSE events.
func emitCopilotSSE(w http.ResponseWriter, flusher http.Flusher, text string) {
	fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":%d,\"model\":\"gpt-5-mini\"}}\n\n", time.Now().Unix())
	flusher.Flush()
	fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"%s\"}\n\n", text)
	flusher.Flush()
	fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":%d,\"model\":\"gpt-5-mini\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3},\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"%s\"}]}]}}\n\n", time.Now().Unix(), text)
	flusher.Flush()
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func TestCopilotExecute_StreamsSSEAndTranslates(t *testing.T) {
	t.Helper()

	var (
		mu        sync.Mutex
		gotBody   []byte
		gotHeader http.Header
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		mu.Lock()
		gotBody = body
		gotHeader = r.Header.Clone()
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		emitCopilotSSE(w, flusher, "ok")
	})

	fake := httptest.NewServer(mux)
	defer fake.Close()

	auth := newTestCopilotAuth(fake.URL)
	payload := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-mini", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}

	exec := NewCopilotExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty payload")
	}

	mu.Lock()
	bodyCopy := append([]byte(nil), gotBody...)
	headerCopy := gotHeader.Clone()
	mu.Unlock()

	if !gjson.GetBytes(bodyCopy, "stream").Bool() {
		t.Fatalf("expected stream flag true in upstream body, got: %s", bodyCopy)
	}
	if accept := headerCopy.Get("Accept"); accept != "text/event-stream" {
		t.Fatalf("expected Accept text/event-stream, got %q", accept)
	}
	if ct := headerCopy.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	if got := headerCopy.Get("X-Initiator"); got != "user" {
		t.Fatalf("expected X-Initiator user, got %q", got)
	}
	if headerCopy.Get("copilot-vision-request") != "" {
		t.Fatalf("unexpected vision header present")
	}
	if got := headerCopy.Get("copilot-integration-id"); got != "vscode-chat" {
		t.Fatalf("expected copilot-integration-id vscode-chat, got %q", got)
	}
	if got := headerCopy.Get("x-vscode-user-agent-library-version"); got != "electron-fetch" {
		t.Fatalf("expected x-vscode-user-agent-library-version electron-fetch, got %q", got)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Payload, &parsed); err != nil {
		t.Fatalf("invalid json payload: %v", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected response content: %+v", parsed)
	}
	if parsed.Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected finish reason: %s", parsed.Choices[0].FinishReason)
	}
}

func TestCopilotExecute_SetsAgentAndVisionHeaders(t *testing.T) {
	t.Helper()

	var (
		mu        sync.Mutex
		gotBody   []byte
		gotHeader http.Header
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotHeader = r.Header.Clone()
		gotBody = append([]byte(nil), body...)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emitCopilotSSE(w, flusher, "agent")
	})

	fake := httptest.NewServer(mux)
	defer fake.Close()

	auth := newTestCopilotAuth(fake.URL)
	payload := []byte(`{"model":"gpt-5-mini","messages":[{"role":"assistant","content":"tool reply"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"do","arguments":"{\"k\":\"v\"}"}}],"tool_choice":{"type":"function","function":{"name":"do"}}}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-mini", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}

	exec := NewCopilotExecutor(&config.Config{})
	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("execute error: %v", err)
	}

	mu.Lock()
	headerCopy := gotHeader.Clone()
	bodyCopy := append([]byte(nil), gotBody...)
	mu.Unlock()

	if got := headerCopy.Get("X-Initiator"); got != "agent" {
		t.Fatalf("expected X-Initiator agent, got %q", got)
	}
	if got := headerCopy.Get("copilot-vision-request"); got != "true" {
		t.Fatalf("expected copilot-vision-request true, got %q", got)
	}
	if got := headerCopy.Get("copilot-integration-id"); got != "vscode-chat" {
		t.Fatalf("expected copilot-integration-id vscode-chat, got %q", got)
	}
	if got := headerCopy.Get("x-vscode-user-agent-library-version"); got != "electron-fetch" {
		t.Fatalf("expected x-vscode-user-agent-library-version electron-fetch, got %q", got)
	}
	if !gjson.GetBytes(bodyCopy, "tool_calls").Exists() {
		t.Fatalf("expected tool_calls to be present in upstream payload: %s", bodyCopy)
	}
	if !gjson.GetBytes(bodyCopy, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be present in upstream payload: %s", bodyCopy)
	}
}

func TestCopilotExecuteStream_TranslatesChunks(t *testing.T) {
	t.Helper()

	var (
		mu        sync.Mutex
		gotHeader http.Header
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		mu.Lock()
		gotHeader = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emitCopilotSSE(w, flusher, "chunk")
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()

	auth := newTestCopilotAuth(fake.URL)
	payload := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-mini", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}

	exec := NewCopilotExecutor(&config.Config{})
	stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var chunks []string
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if len(chunks) == 0 {
		t.Fatalf("expected at least one chunk")
	}
	foundContent := false
	for _, c := range chunks {
		if strings.Contains(c, `"delta":{"content":"chunk"`) || strings.Contains(c, `"content":"chunk"`) {
			foundContent = true
			break
		}
	}
	if !foundContent {
		t.Fatalf("stream chunks missing expected content: %v", chunks)
	}

	mu.Lock()
	headerCopy := gotHeader.Clone()
	mu.Unlock()

	if got := headerCopy.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("expected Accept text/event-stream, got %q", got)
	}
	if got := headerCopy.Get("copilot-integration-id"); got != "vscode-chat" {
		t.Fatalf("expected copilot-integration-id vscode-chat, got %q", got)
	}
	if got := headerCopy.Get("x-vscode-user-agent-library-version"); got != "electron-fetch" {
		t.Fatalf("expected x-vscode-user-agent-library-version electron-fetch, got %q", got)
	}
}

func TestCopilotExecute_AggregatesOpenAIStyleSSE(t *testing.T) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected http.Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"created\":1700000000,\"model\":\"gpt-5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"created\":1700000000,\"model\":\"gpt-5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"created\":1700000000,\"model\":\"gpt-5-mini\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"completion_tokens\":5,\"prompt_tokens\":3,\"total_tokens\":8}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	auth := newTestCopilotAuth(server.URL)
	payload := []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-mini", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: payload}

	exec := NewCopilotExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	var parsed struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(resp.Payload, &parsed); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if parsed.ID != "chatcmpl-abc" {
		t.Fatalf("unexpected response id: %q", parsed.ID)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "Hello" {
		t.Fatalf("expected aggregated message \"Hello\", got %+v", parsed.Choices)
	}
	if parsed.Choices[0].FinishReason != "stop" {
		t.Fatalf("expected finish_reason stop, got %q", parsed.Choices[0].FinishReason)
	}
	if parsed.Usage.TotalTokens != 8 {
		t.Fatalf("expected usage total_tokens=8, got %d", parsed.Usage.TotalTokens)
	}
}
