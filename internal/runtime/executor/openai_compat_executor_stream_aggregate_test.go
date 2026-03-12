package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutor_ForceUpstreamStreamAggregatesReasoningAndContent(t *testing.T) {
	var gotAccept string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"r1\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"there\"}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:                "iamai",
		BaseURL:             server.URL + "/v1",
		ForceUpstreamStream: true,
		Models: []config.OpenAICompatibilityModel{{
			Name:  "glm-5",
			Alias: "glm-5",
		}},
	}}}
	
	executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "iamai",
	}}
	payload := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("expected Accept text/event-stream, got %q", gotAccept)
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("expected upstream payload to include stream=true")
	}
	if !gjson.ValidBytes(resp.Payload) {
		t.Fatalf("expected valid JSON response, got: %s", string(resp.Payload))
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.content").String() != "hi there" {
		t.Fatalf("content mismatch: %s", gjson.GetBytes(resp.Payload, "choices.0.message.content").String())
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String() != "r1" {
		t.Fatalf("reasoning mismatch: %s", gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String())
	}
	if gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("expected finish_reason stop, got %s", gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String())
	}
	if gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int() != 1 {
		t.Fatalf("expected usage prompt_tokens")
	}
}

func TestOpenAICompatExecutor_ForceUpstreamStreamAggregatesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1710000001,\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\": \"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1710000001,\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"/tmp/test\\\"}\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:                "iamai",
		BaseURL:             server.URL + "/v1",
		ForceUpstreamStream: true,
		Models: []config.OpenAICompatibilityModel{{
			Name:  "glm-5",
			Alias: "glm-5",
		}},
	}}}
	
	executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "iamai",
	}}
	payload := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !gjson.ValidBytes(resp.Payload) {
		t.Fatalf("expected valid JSON response")
	}
	calls := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls")
	if !calls.Exists() || len(calls.Array()) != 1 {
		t.Fatalf("expected one tool_call, got: %s", calls.String())
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String() != "read" {
		t.Fatalf("tool_call name mismatch")
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.arguments").String() != `{"path": "/tmp/test"}` {
		t.Fatalf("tool_call arguments mismatch: %s", gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.arguments").String())
	}
}

func TestOpenAICompatExecutor_DefaultBehaviorUnchanged(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-3","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "iamai",
		BaseURL: server.URL + "/v1",
		Models: []config.OpenAICompatibilityModel{{
			Name:  "glm-5",
			Alias: "glm-5",
		}},
	}}}
	
	executor := NewOpenAICompatExecutor("openai-compatibility", cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "iamai",
	}}
	payload := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("did not expect stream=true in payload")
	}
	if !gjson.ValidBytes(resp.Payload) {
		t.Fatalf("expected valid JSON response")
	}
}
