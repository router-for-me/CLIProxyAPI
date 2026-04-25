package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorCompactPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses/compact")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected input in body")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected messages in body")
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorExecuteSetsJSONAcceptHeader(t *testing.T) {
	var gotPath string
	var gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("minimax", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"minimax-m2.5","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "minimax-m2.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if gotAccept != "application/json" {
		t.Fatalf("accept = %q, want application/json", gotAccept)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty payload")
	}
}

func TestOpenAICompatExecutorCompactDisabledByConfig(t *testing.T) {
	disabled := false
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		ResponsesCompactEnabled: &disabled,
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "https://example.com/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected compact-disabled error, got nil")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", se.StatusCode(), http.StatusNotFound)
	}
}

func TestOpenAICompatExecutorExecuteStreamFallsBackFrom406ForResponsesClients(t *testing.T) {
	requestCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, _ := io.ReadAll(r.Body)

		switch requestCount {
		case 1:
			if got := r.Header.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("expected stream Accept header, got %q", got)
			}
			if !strings.Contains(string(body), `"stream":true`) {
				t.Fatalf("expected initial upstream request to keep stream=true, got %s", body)
			}
			http.Error(w, "status 406", http.StatusNotAcceptable)
		case 2:
			if got := r.Header.Get("Accept"); got != "application/json" {
				t.Fatalf("expected fallback Accept header, got %q", got)
			}
			if strings.Contains(string(body), `"stream":true`) {
				t.Fatalf("expected fallback request to disable stream, got %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl_fallback","object":"chat.completion","created":1735689600,"model":"minimax-m2.5","choices":[{"index":0,"message":{"role":"assistant","content":"hi from fallback"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
		default:
			t.Fatalf("unexpected upstream call %d", requestCount)
		}
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("minimax", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": upstream.URL + "/v1",
		"api_key":  "test",
	}}
	originalRequest := []byte(`{"model":"minimax-m2.5","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "minimax-m2.5",
		Payload: originalRequest,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: originalRequest,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	var payloads [][]byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payloads = append(payloads, append([]byte(nil), chunk.Payload...))
	}

	if requestCount != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", requestCount)
	}
	if len(payloads) != 1 {
		t.Fatalf("expected exactly one synthesized SSE payload, got %d", len(payloads))
	}

	got := string(payloads[0])
	if !strings.Contains(got, "event: response.completed") {
		t.Fatalf("expected synthesized response.completed event, got %q", got)
	}
	if !strings.Contains(got, `"status":"completed"`) {
		t.Fatalf("expected completed status in synthesized payload, got %q", got)
	}
	if !strings.Contains(got, "hi from fallback") {
		t.Fatalf("expected assistant text in synthesized payload, got %q", got)
	}
}

func TestConvertChatCompletionToResponsesObjectUnwrapsDataEnvelope(t *testing.T) {
	payload := []byte(`{
		"success": true,
		"data": {
			"id":"chatcmpl_env",
			"created":1735689600,
			"model":"minimax-m2.5",
			"choices":[{"index":0,"message":{"role":"assistant","content":"wrapped hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
		}
	}`)

	got, err := convertChatCompletionToResponsesObject(payload)
	if err != nil {
		t.Fatalf("convertChatCompletionToResponsesObject returned error: %v", err)
	}

	if text := gjson.GetBytes(got, "output.0.content.0.text").String(); text != "wrapped hello" {
		t.Fatalf("expected wrapped text, got %q in %s", text, got)
	}
	if model := gjson.GetBytes(got, "model").String(); model != "minimax-m2.5" {
		t.Fatalf("expected wrapped model, got %q", model)
	}
}

func TestSynthesizeOpenAIResponsesCompletionEventUnwrapsWrappedChatCompletion(t *testing.T) {
	payload := []byte(`{
		"success": true,
		"data": {
			"id":"chatcmpl_env",
			"object":"chat.completion",
			"created":1735689600,
			"model":"minimax-m2.5",
			"choices":[{"index":0,"message":{"role":"assistant","content":"wrapped stream hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}
		}
	}`)

	got, err := synthesizeOpenAIResponsesCompletionEvent(payload)
	if err != nil {
		t.Fatalf("synthesizeOpenAIResponsesCompletionEvent returned error: %v", err)
	}
	if !strings.Contains(string(got), "wrapped stream hello") {
		t.Fatalf("expected wrapped assistant text in synthesized SSE payload, got %q", got)
	}
}
