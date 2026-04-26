package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestValidateOpenAICompatChatCompletionResponseProtocol_DetectsDanglingToolFinishReason(t *testing.T) {
	err := validateOpenAICompatChatCompletionResponseProtocol(
		"openai-compatibility",
		"Kimi-K2-Thinking",
		http.Header{"X-Request-Id": []string{"req_invalid"}},
		[]byte(`{"id":"chatcmpl_1","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"<think>"}}]}`),
	)
	if err == nil {
		t.Fatal("expected protocol violation")
	}
	if !strings.Contains(err.Error(), "protocol violation") {
		t.Fatalf("error = %v, want protocol violation", err)
	}
}

func TestValidateOpenAICompatChatCompletionResponseProtocol_AllowsToolCallsPayload(t *testing.T) {
	err := validateOpenAICompatChatCompletionResponseProtocol(
		"openai-compatibility",
		"Kimi-K2.5",
		http.Header{"X-Request-Id": []string{"req_valid"}},
		[]byte(`{"id":"chatcmpl_1","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"write","arguments":"{}"}}]}}]}`),
	)
	if err != nil {
		t.Fatalf("expected valid tool_calls payload, got %v", err)
	}
}

func TestOpenAICompatExecutorExecute_ProtocolViolationOpensCircuitBreaker(t *testing.T) {
	const (
		providerName = "cb-openai-compat-protocol"
		authID       = "cb-openai-compat-protocol-auth"
		modelID      = "Kimi-K2-Thinking"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_protocol_nonstream")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_invalid","object":"chat.completion","created":1775540000,"model":"Kimi-K2-Thinking","choices":[{"index":0,"message":{"role":"assistant","content":"<think>broken</think>"},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           providerName,
			CircuitBreakerFailureThreshold: 1,
		}},
	})
	auth := &cliproxyauth.Auth{
		ID:       authID,
		Provider: providerName,
		Attributes: map[string]string{
			"base_url":     upstream.URL + "/v1",
			"api_key":      "test-key",
			"compat_name":  providerName,
			"provider_key": providerName,
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected protocol violation error")
	}
	if !strings.Contains(err.Error(), "protocol violation") {
		t.Fatalf("error = %v, want protocol violation", err)
	}
	statusCoder, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", err)
	}
	if statusCoder.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", statusCoder.StatusCode(), http.StatusBadGateway)
	}
	if !reg.IsCircuitOpen(authID, modelID) {
		t.Fatalf("expected circuit to open for model %q after protocol violation", modelID)
	}
}

func TestOpenAICompatExecutorExecuteStream_DanglingToolFinishReasonReturnsProtocolViolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req_protocol_stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_invalid\",\"object\":\"chat.completion.chunk\",\"created\":1775540000,\"model\":\"Kimi-K2-Thinking\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"<think>broken</think>\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl_invalid\",\"object\":\"chat.completion.chunk\",\"created\":1775540000,\"model\":\"Kimi-K2-Thinking\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "test-openai-compat-stream-protocol",
		Attributes: map[string]string{
			"base_url": upstream.URL + "/v1",
			"api_key":  "test",
		},
	}

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "Kimi-K2-Thinking",
		Payload: []byte(`{"model":"Kimi-K2-Thinking","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var gotErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}

	if gotErr == nil {
		t.Fatal("expected protocol violation error in stream")
	}
	if !strings.Contains(gotErr.Error(), "protocol violation") {
		t.Fatalf("error = %v, want protocol violation", gotErr)
	}
	statusCoder, ok := gotErr.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", gotErr)
	}
	if statusCoder.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", statusCoder.StatusCode(), http.StatusBadGateway)
	}
}
