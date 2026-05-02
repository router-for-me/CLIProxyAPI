package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutor_AutoUsesResponsesEndpointForResponsesRequests(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:         "openrouter",
			BaseURL:      server.URL + "/v1",
			EndpointMode: "auto",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "openrouter",
			"provider_key": "openrouter",
		},
	}
	payload := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses payload to keep input field")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat completions messages field in responses payload")
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutor_DefaultModeKeepsLegacyChatCompletionsRouting(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "openrouter",
			BaseURL: server.URL + "/v1",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "openrouter",
			"provider_key": "openrouter",
		},
	}
	payload := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if !gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("expected legacy chat completions payload to include messages")
	}
	if gotObject := gjson.GetBytes(resp.Payload, "object").String(); gotObject != "response" {
		t.Fatalf("response object = %q, want %q", gotObject, "response")
	}
}

func TestOpenAICompatExecutor_ChatCompletionsOverrideTranslatesResponsesRequests(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:         "openrouter",
			BaseURL:      server.URL + "/v1",
			EndpointMode: "chat-completions",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "openrouter",
			"provider_key": "openrouter",
		},
	}
	payload := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if !gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("expected chat completions payload to include messages")
	}
	if gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("unexpected responses input field in chat completions payload")
	}
	if gotObject := gjson.GetBytes(resp.Payload, "object").String(); gotObject != "response" {
		t.Fatalf("response object = %q, want %q", gotObject, "response")
	}
}

func TestOpenAICompatExecutor_ResponsesOverrideTranslatesChatRequests(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:         "openrouter",
			BaseURL:      server.URL + "/v1",
			EndpointMode: "responses",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "openrouter",
			"provider_key": "openrouter",
		},
	}
	payload := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected responses payload to include input")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected chat completions messages field in responses payload")
	}
	if gotObject := gjson.GetBytes(resp.Payload, "object").String(); gotObject != "chat.completion" {
		t.Fatalf("response object = %q, want %q", gotObject, "chat.completion")
	}
}

func TestOpenAICompatExecutor_StreamResponsesPassthroughPreservesEvents(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:         "openrouter",
			BaseURL:      server.URL + "/v1",
			EndpointMode: "auto",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "openrouter",
			"provider_key": "openrouter",
		},
	}
	payload := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"hi"}],"stream":true}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses")
	}
	if strings.Contains(string(gotBody), "stream_options") {
		t.Fatalf("responses upstream request unexpectedly added chat-completions stream_options: %s", string(gotBody))
	}

	var chunks [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk.Payload)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want %d", len(chunks), 2)
	}
	if got := string(chunks[0]); got != "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}" {
		t.Fatalf("first chunk = %q", got)
	}
	if got := string(chunks[1]); got != "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}" {
		t.Fatalf("second chunk = %q", got)
	}
}
