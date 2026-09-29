package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const mirasimTestClaudeResponse = `{"id":"msg_1","type":"message","model":"claude-sonnet-4","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// newMirasimTestUpstream starts a fake Anthropic Messages upstream and captures
// the last request it received. When the incoming body asks for a stream it
// replies with a minimal Claude SSE sequence, otherwise with a JSON message.
func newMirasimTestUpstream(t *testing.T) (*httptest.Server, func() (path string, headers http.Header, body []byte)) {
	t.Helper()
	var seenPath string
	var seenHeaders http.Header
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		if gjson.GetBytes(seenBody, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_start\n" +
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
				"event: content_block_start\n" +
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
				"event: content_block_stop\n" +
				`data: {"type":"content_block_stop","index":0}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
				"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirasimTestClaudeResponse))
	}))
	t.Cleanup(server.Close)
	return server, func() (string, http.Header, []byte) {
		return seenPath, seenHeaders, seenBody
	}
}

func mirasimTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "mirasim",
		Attributes: map[string]string{
			"api_key":  "mirasim-test-key",
			"base_url": baseURL,
		},
	}
}

func assertMirasimRequestShape(t *testing.T, path string, headers http.Header, body []byte) {
	t.Helper()
	if path != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", path)
	}
	if got := headers.Get("Authorization"); got != "Bearer mirasim-test-key" {
		t.Fatalf("Authorization = %q, want Bearer mirasim-test-key", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want empty for mirasim upstream", got)
	}
	if !gjson.GetBytes(body, "messages").IsArray() {
		t.Fatalf("upstream body missing messages array: %s", body)
	}
	if got := gjson.GetBytes(body, "model").String(); got == "" {
		t.Fatalf("upstream body missing model: %s", body)
	}
}

func TestMirasimExecutor_OpenAISourceUsesBearerOnCustomBaseURL(t *testing.T) {
	server, captured := newMirasimTestUpstream(t)
	executor := NewMirasimExecutor(&config.Config{})
	auth := mirasimTestAuth(server.URL)
	payload := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	path, headers, body := captured()
	assertMirasimRequestShape(t, path, headers, body)
	if got := gjson.GetBytes(resp.Payload, "choices").Array(); len(got) != 1 {
		t.Fatalf("response choices count = %d, want 1: %s", len(got), resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "ok" {
		t.Fatalf("response content = %q, want ok: %s", got, resp.Payload)
	}
}

func TestMirasimExecutor_ClaudeSourceUsesBearerOnCustomBaseURL(t *testing.T) {
	server, captured := newMirasimTestUpstream(t)
	executor := NewMirasimExecutor(&config.Config{})
	auth := mirasimTestAuth(server.URL)
	payload := []byte(`{"model":"claude-sonnet-4","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	path, headers, body := captured()
	assertMirasimRequestShape(t, path, headers, body)
	if got := gjson.GetBytes(resp.Payload, "type").String(); got != "message" {
		t.Fatalf("response type = %q, want message: %s", got, resp.Payload)
	}
}

func TestMirasimExecutor_ExecuteStreamUsesBearerOnCustomBaseURL(t *testing.T) {
	server, captured := newMirasimTestUpstream(t)
	executor := NewMirasimExecutor(&config.Config{})
	auth := mirasimTestAuth(server.URL)
	payload := []byte(`{"model":"claude-sonnet-4","max_tokens":128,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	path, headers, body := captured()
	assertMirasimRequestShape(t, path, headers, body)
}

func TestMirasimExecutor_MissingBaseURLErrors(t *testing.T) {
	executor := NewMirasimExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "mirasim",
		Attributes: map[string]string{"api_key": "mirasim-test-key"},
	}
	payload := []byte(`{"model":"claude-sonnet-4","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errExecute == nil || !strings.Contains(errExecute.Error(), "base_url") {
		t.Fatalf("Execute() error = %v, want base_url required error", errExecute)
	}

	_, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errStream == nil || !strings.Contains(errStream.Error(), "base_url") {
		t.Fatalf("ExecuteStream() error = %v, want base_url required error", errStream)
	}

	_, errCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount == nil || !strings.Contains(errCount.Error(), "base_url") {
		t.Fatalf("CountTokens() error = %v, want base_url required error", errCount)
	}
}

func TestMirasimExecutor_Identifier(t *testing.T) {
	if got := NewMirasimExecutor(&config.Config{}).Identifier(); got != "mirasim" {
		t.Fatalf("Identifier() = %q, want mirasim", got)
	}
}
