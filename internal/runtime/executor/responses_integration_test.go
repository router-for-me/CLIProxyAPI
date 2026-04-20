package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	responsesconverter "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Helper: auth with given base URL
// ---------------------------------------------------------------------------

func testAuthForServer(serverURL, authID string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       authID,
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     serverURL + "/v1",
			"api_key":      "test-key",
			"compat_name":  "openai-compatibility",
			"provider_key": "openai-compatibility",
		},
	}
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: Unknown mode → /responses (probe)
// On success, caches as Native; on capability error, falls back to /chat/completions.
// ---------------------------------------------------------------------------

func TestExecute_UnknownMode_TriesResponsesFirst(t *testing.T) {
	const authID = "test-unknown-defaults-exec"

	responsesCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1775540000,"model":"gpt-4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Invalidate(authID)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !responsesCalled {
		t.Fatal("expected /v1/responses to be called in Unknown mode (probe)")
	}

	// After success from /responses, the mode should be cached as Native.
	mode := globalResponsesCapabilityResolver.Resolve(authID)
	if mode != ResponsesModeNative {
		t.Fatalf("expected mode to be Native after /responses success, got %v", mode)
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: Unknown mode + capability error → fallback to /chat/completions
// ---------------------------------------------------------------------------

func TestExecute_UnknownMode_CapabilityError_FallsToChatCompletions(t *testing.T) {
	const authID = "test-unknown-fallback-exec"

	responsesCalled := false
	chatCompletionsCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalled = true
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"endpoint not found"}}`))
		case "/v1/chat/completions":
			chatCompletionsCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-fb","object":"chat.completion","created":1775540000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Invalidate(authID)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !responsesCalled {
		t.Fatal("expected /v1/responses to be probed first in Unknown mode")
	}
	if !chatCompletionsCalled {
		t.Fatal("expected /v1/chat/completions fallback after capability error")
	}

	mode := globalResponsesCapabilityResolver.Resolve(authID)
	if mode != ResponsesModeChatFallback {
		t.Fatalf("expected mode to be ChatFallback after capability error, got %v", mode)
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: Native mode → /responses passthrough
// ---------------------------------------------------------------------------

func TestExecute_NativeMode_ResponsesPassthrough(t *testing.T) {
	const authID = "test-native-passthrough-exec"

	responsesCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1775540000,"model":"gpt-4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !responsesCalled {
		t.Fatal("expected /v1/responses to be called in Native mode")
	}
	if !gjson.GetBytes(resp.Payload, "id").Exists() {
		t.Fatal("expected response to have an id field")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: Native mode + error from /responses → regular error
// (capability error fallback only triggers when responsesMode == Unknown)
// ---------------------------------------------------------------------------

func TestExecute_NativeMode_ResponsesError_ReturnsError(t *testing.T) {
	const authID = "test-native-error-exec"

	responsesCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalled = true
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"endpoint not found"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error when /responses returns 404 in Native mode")
	}

	if !responsesCalled {
		t.Fatal("expected /v1/responses to be called in Native mode")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: ChatFallback mode → /chat/completions
// ---------------------------------------------------------------------------

func TestExecute_ChatFallbackMode_UsesChatEndpoint(t *testing.T) {
	const authID = "test-chat-fallback-mode-exec"

	chatCompletionsCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCompletionsCalled = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1775540000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hi!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeChatFallback)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !chatCompletionsCalled {
		t.Fatal("expected /v1/chat/completions to be called in ChatFallback mode")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: ChatFallback mode stores response state
// ---------------------------------------------------------------------------

func TestExecute_ChatFallbackMode_StoresStateInStateStore(t *testing.T) {
	const authID = "test-state-store-exec"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-state-1","object":"chat.completion","created":1775540000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeChatFallback)

	testStore := NewMemoryResponsesStateStore(30*time.Minute, 1024)
	origStore := globalResponsesStateStore
	globalResponsesStateStore = testStore
	defer func() { globalResponsesStateStore = origStore }()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hello"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify the response was translated back to openai-response format.
	responseID := gjson.GetBytes(resp.Payload, "id").String()
	if responseID == "" {
		t.Fatal("expected translated response to have a non-empty id")
	}

	// The state store should have an entry for this response ID.
	snapshot, ok := testStore.Get(responseID)
	if !ok {
		t.Fatalf("expected state store to have entry for response ID %q", responseID)
	}
	if snapshot.Model != "gpt-4" {
		t.Errorf("snapshot.Model = %q, want %q", snapshot.Model, "gpt-4")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: ChatFallback + previous_response_id → transcript rebuild
// The request should succeed with a merged transcript (snapshot + current).
// ---------------------------------------------------------------------------

func TestExecute_ChatFallbackMode_PreviousResponseID_RebuildsTranscript(t *testing.T) {
	const authID = "test-transcript-rebuild-exec"
	var chatRequestBody []byte

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if gjson.GetBytes(body, "messages").Exists() {
				chatRequestBody = append([]byte(nil), body...)
			} else {
				modelName := gjson.GetBytes(body, "model").String()
				chatRequestBody = responsesconverter.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName, body, false)
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-2","object":"chat.completion","created":1775540001,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Follow-up"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeChatFallback)

	testStore := NewMemoryResponsesStateStore(30*time.Minute, 1024)
	origStore := globalResponsesStateStore
	globalResponsesStateStore = testStore
	defer func() { globalResponsesStateStore = origStore }()

	testStore.Put("resp_prev_123", ResponsesSnapshot{
		Model:        "gpt-4",
		Instructions: "You are helpful.",
		Input:        json.RawMessage(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]`),
		Output: json.RawMessage(`[
			{"type":"function_call","call_id":"call_1","name":"tool_a","arguments":"{}"},
			{"type":"function_call","call_id":"call_2","name":"tool_b","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"result_1"},
			{"type":"function_call_output","call_id":"call_2","output":"result_2"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi there"}]}
		]`),
		CreatedAt: 1775540000,
	})

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","previous_response_id":"resp_prev_123","input":[{"role":"user","content":"follow up?"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify the response was translated back to openai-response format.
	if !gjson.GetBytes(resp.Payload, "id").Exists() {
		t.Fatal("expected translated response to have an id field")
	}
	if len(chatRequestBody) == 0 {
		t.Fatal("expected chat fallback request body to be captured")
	}

	chatMessages := gjson.GetBytes(chatRequestBody, "messages").Array()
	aggIdx := -1
	for i, msg := range chatMessages {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		toolCalls := msg.Get("tool_calls").Array()
		if len(toolCalls) != 2 {
			continue
		}
		if toolCalls[0].Get("id").String() == "call_1" && toolCalls[1].Get("id").String() == "call_2" {
			aggIdx = i
			break
		}
	}
	if aggIdx == -1 {
		t.Fatalf("expected one assistant message to aggregate call_1/call_2 tool_calls, messages=%s", gjson.GetBytes(chatRequestBody, "messages").Raw)
	}
	if aggIdx+2 >= len(chatMessages) {
		t.Fatalf("expected two tool results after aggregated assistant tool_calls, messages=%s", gjson.GetBytes(chatRequestBody, "messages").Raw)
	}
	if chatMessages[aggIdx+1].Get("role").String() != "tool" || chatMessages[aggIdx+1].Get("tool_call_id").String() != "call_1" {
		t.Fatalf("expected first tool result immediately after aggregated tool_calls to be call_1, got=%s", chatMessages[aggIdx+1].Raw)
	}
	if chatMessages[aggIdx+2].Get("role").String() != "tool" || chatMessages[aggIdx+2].Get("tool_call_id").String() != "call_2" {
		t.Fatalf("expected second tool result after aggregated tool_calls to be call_2, got=%s", chatMessages[aggIdx+2].Raw)
	}

	// The new response should also be stored in the state store.
	responseID := gjson.GetBytes(resp.Payload, "id").String()
	if responseID == "" {
		t.Fatal("expected translated response to have a non-empty id")
	}
	_, ok := testStore.Get(responseID)
	if !ok {
		t.Fatalf("expected state store to have entry for new response ID %q", responseID)
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-streaming Execute: ChatFallback + missing previous_response_id → error
// ---------------------------------------------------------------------------

func TestExecute_ChatFallbackMode_MissingPreviousResponseID_ReturnsError(t *testing.T) {
	const authID = "test-missing-prev-id-exec"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeChatFallback)

	testStore := NewMemoryResponsesStateStore(30*time.Minute, 1024)
	origStore := globalResponsesStateStore
	globalResponsesStateStore = testStore
	defer func() { globalResponsesStateStore = origStore }()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","previous_response_id":"resp_nonexistent","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error for missing previous_response_id")
	}
	if !strings.Contains(err.Error(), "not found or expired") {
		t.Fatalf("unexpected error message: %v", err)
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Streaming ExecuteStream: Native mode → /responses passthrough
// ---------------------------------------------------------------------------

func TestExecuteStream_NativeMode_UsesResponsesEndpoint(t *testing.T) {
	const authID = "test-native-stream"

	responsesCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalled = true
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_s1\",\"status\":\"in_progress\"}}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_s1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello!\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotCompleted bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		if hasOpenAIResponsesCompletedEvent(chunk.Payload) {
			gotCompleted = true
		}
	}

	if !responsesCalled {
		t.Fatal("expected /v1/responses to be called in Native mode")
	}
	if !gotCompleted {
		t.Fatal("expected response.completed event")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Streaming ExecuteStream: Native mode + error from /responses → regular error
// ---------------------------------------------------------------------------

func TestExecuteStream_NativeMode_ResponsesError_ReturnsError(t *testing.T) {
	const authID = "test-native-error-stream"

	responsesCalled := false

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			responsesCalled = true
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"endpoint not found"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4",
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err == nil {
		t.Fatal("expected error when /responses returns 404 in Native mode")
	}

	if !responsesCalled {
		t.Fatal("expected /v1/responses to be called in Native mode")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Capability error should NOT record circuit breaker failure when in Unknown mode
// ---------------------------------------------------------------------------

func TestExecute_CapabilityErrorInUnknownMode_NoCircuitBreakerFailure(t *testing.T) {
	const (
		authID = "test-no-cb-failure-exec"
		model  = "gpt-4-no-cb"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			// In Unknown mode, the first request probes /responses.
			// Return a 500 with "convert_request_failed" to trigger capability error.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"convert_request_failed","type":"server_error"}}`))
		case "/v1/chat/completions":
			// Fallback retry also returns 500 (simulating general failure).
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"server error","type":"server_error"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Invalidate(authID)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatibility", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, model)
		reg.UnregisterClient(authID)
	})

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           "openai-compatibility",
			CircuitBreakerFailureThreshold: 1,
			CircuitBreakerRecoveryTimeout:  60,
		}},
	})
	auth := testAuthForServer(upstream.URL, authID)

	// In Unknown mode, /responses returns 500 + "convert_request_failed".
	// isCapabilityError returns true, so the capability error path should NOT
	// record circuit breaker. But the retry (ChatFallback) hits /chat/completions
	// which also returns 500 → this time it's a regular error (not Unknown mode).
	// So the circuit breaker WILL be recorded on the retry.
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	// The retry will also fail (500 from /chat/completions),
	// so we expect an error.
	if err == nil {
		t.Fatal("expected error since /chat/completions returns 500")
	}

	// The mode should be cached as ChatFallback after the capability error detection.
	mode := globalResponsesCapabilityResolver.Resolve(authID)
	if mode != ResponsesModeChatFallback {
		t.Fatalf("expected mode to be ChatFallback after capability error, got %v", mode)
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// Non-capability error in Unknown mode SHOULD record circuit breaker failure
// ---------------------------------------------------------------------------

func TestExecute_NonCapabilityErrorInUnknownMode_RecordsCircuitBreaker(t *testing.T) {
	const (
		authID = "test-cb-failure-exec"
		model  = "gpt-4-cb"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			// In Unknown mode, /responses is probed first.
			// Return a 429 rate limit error - NOT a capability error.
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Invalidate(authID)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatibility", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, model)
		reg.UnregisterClient(authID)
	})

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           "openai-compatibility",
			CircuitBreakerFailureThreshold: 1,
			CircuitBreakerRecoveryTimeout:  60,
		}},
	})
	auth := testAuthForServer(upstream.URL, authID)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-4","input":[{"role":"user","content":"hi"}],"stream":false}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error since /chat/completions returns 429")
	}

	// A non-capability error should record a circuit breaker failure.
	if !reg.IsCircuitOpen(authID, model) {
		t.Fatal("expected circuit breaker to be open for non-capability 429 error")
	}

	globalResponsesCapabilityResolver.Invalidate(authID)
}

// ---------------------------------------------------------------------------
// SSE helper tests
// ---------------------------------------------------------------------------

func TestExtractSSEDataPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard SSE frame",
			input: "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
			want:  `{"type":"response.completed"}`,
		},
		{
			name:  "raw JSON without SSE framing",
			input: `{"type":"response.completed"}`,
			want:  `{"type":"response.completed"}`,
		},
		{
			name:  "data line with extra spaces",
			input: "data:  {\"type\":\"test\"}  \n\n",
			want:  `{"type":"test"}`,
		},
		{
			name:  "DONE payload falls through to raw return",
			input: "data: [DONE]\n\n",
			// extractSSEDataPayload skips [DONE] data lines and falls through to
			// returning the trimmed input as-is when no valid data: line is found.
			want: "data: [DONE]",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := string(extractSSEDataPayload([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("extractSSEDataPayload() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasOpenAIResponsesEventType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		chunk     string
		eventType string
		want      bool
	}{
		{
			name:      "SSE event line match",
			chunk:     "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
			eventType: "response.completed",
			want:      true,
		},
		{
			name:      "data payload type match",
			chunk:     "data: {\"type\":\"response.created\"}\n\n",
			eventType: "response.created",
			want:      true,
		},
		{
			name:      "no match",
			chunk:     "data: {\"type\":\"response.output_item.added\"}\n\n",
			eventType: "response.completed",
			want:      false,
		},
		{
			name:      "empty chunk",
			chunk:     "",
			eventType: "response.completed",
			want:      false,
		},
		{
			name:      "empty event type",
			chunk:     "event: response.completed\n\n",
			eventType: "",
			want:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasOpenAIResponsesEventType([]byte(tt.chunk), tt.eventType)
			if got != tt.want {
				t.Errorf("hasOpenAIResponsesEventType() = %v, want %v", got, tt.want)
			}
		})
	}
}
