package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestCodexStreamDisconnectAfterHeadersFailsOver reproduces issue #6247:
// When upstream A sends HTTP 200 headers and closes the connection before emitting
// any SSE bytes, the gateway must fail over to credential B instead of aborting
// with invalid_request_error.
func TestCodexStreamDisconnectAfterHeadersFailsOver(t *testing.T) {
	for _, buffering := range []bool{false, true} {
		name := "unbuffered"
		if buffering {
			name = "buffered"
		}
		t.Run(name, func(t *testing.T) {
			const model = "gpt-6-astra"
			var mu sync.Mutex
			var attempts []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				mu.Lock()
				attempts = append(attempts, account)
				mu.Unlock()

				if account == "account-a" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					// Disconnect after headers but before any SSE bytes.
					return
				}

				// Account B completes the stream.
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_b\",\"model\":%q}}\n\n", model)
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_b\",\"type\":\"message\"}}\n\n")
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_b\",\"delta\":\"MOCK_B\"}\n\n")
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_b\",\"status\":\"completed\",\"output\":[]}}\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}))
			defer server.Close()

			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 2)
			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = buffering
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))

			const authA = "codex-failover-a"
			const authB = "codex-failover-b"
			registry.GetGlobalRegistry().RegisterClient(authA, "codex", []*registry.ModelInfo{{ID: model}})
			registry.GetGlobalRegistry().RegisterClient(authB, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(authA)
				registry.GetGlobalRegistry().UnregisterClient(authB)
			})

			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID: authA, Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"priority": "100", "base_url": server.URL, "api_key": "account-a"},
				Metadata:   map[string]any{"disable_cooling": true},
			}); errRegister != nil {
				t.Fatal(errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID: authB, Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"priority": "0", "base_url": server.URL, "api_key": "account-b"},
				Metadata:   map[string]any{"disable_cooling": true},
			}); errRegister != nil {
				t.Fatal(errRegister)
			}

			// Test ExecuteStream directly via Conductor
			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)),
			}
			opts := cliproxyexecutor.Options{
				Stream:          true,
				SourceFormat:    sdktranslator.FromString("openai-response"),
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: req.Payload,
			}

			streamResult, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream failed instead of failing over: %v", errStream)
			}
			if streamResult == nil {
				t.Fatal("expected non-nil streamResult after failover")
			}

			var streamBody string
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
				streamBody += string(chunk.Payload)
			}

			if !strings.Contains(streamBody, "MOCK_B") || !strings.Contains(streamBody, "response.completed") {
				t.Fatalf("stream body missing expected completion from B: %s", streamBody)
			}

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()

			if len(gotAttempts) != 2 || gotAttempts[0] != "account-a" || gotAttempts[1] != "account-b" {
				t.Fatalf("attempts = %v, want [account-a, account-b]", gotAttempts)
			}
		})
	}
}

// TestCodexStreamDisconnectExhaustionNotInvalidRequestError verifies that when
// retries are exhausted for a stream that disconnects before any SSE bytes,
// the surfaced JSON error is classified as an upstream/server error (not invalid_request_error).
func TestCodexStreamDisconnectExhaustionNotInvalidRequestError(t *testing.T) {
	const model = "gpt-6-astra"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Close without any SSE data bytes.
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	cfg := &config.Config{}
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))

	const authID = "codex-exhaustion-test"
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL, "api_key": "dummy"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)))
	c.Request.Header.Set("Content-Type", "application/json")

	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	openaihandlers.NewOpenAIResponsesAPIHandler(base).Responses(c)

	body := recorder.Body.String()
	var errorResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal([]byte(body), &errorResp); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal JSON error response (body: %s): %v", body, errUnmarshal)
	}

	if errorResp.Error.Type == "invalid_request_error" {
		t.Fatalf("error.type = %q for pre-output disconnect: must not claim well-formed request is invalid", errorResp.Error.Type)
	}
	if errorResp.Error.Type != "server_error" {
		t.Fatalf("error.type = %q, want server_error", errorResp.Error.Type)
	}
}

// TestCodexStreamDisconnectAfterOutputDoesNotFailOver verifies that when upstream A
// disconnects AFTER emitting visible output tokens/deltas, it does NOT attempt credential B
// (replaying partial output is unsafe), and instead surfaces the incomplete stream error chunk.
func TestCodexStreamDisconnectAfterOutputDoesNotFailOver(t *testing.T) {
	for _, buffering := range []bool{false, true} {
		name := "unbuffered"
		if buffering {
			name = "buffered"
		}
		t.Run(name, func(t *testing.T) {
			const model = "gpt-6-astra"
			var mu sync.Mutex
			var attempts []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				mu.Lock()
				attempts = append(attempts, account)
				mu.Unlock()

				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				// Emit initial chunks including output text delta
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\",\"model\":%q}}\n\n", model)
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_a\",\"type\":\"message\"}}\n\n")
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_a\",\"delta\":\"MOCK_A_PARTIAL\"}\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				// Disconnect without response.completed
			}))
			defer server.Close()

			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 2)
			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = buffering
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))

			const authA = "codex-partial-a"
			const authB = "codex-partial-b"
			registry.GetGlobalRegistry().RegisterClient(authA, "codex", []*registry.ModelInfo{{ID: model}})
			registry.GetGlobalRegistry().RegisterClient(authB, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(authA)
				registry.GetGlobalRegistry().UnregisterClient(authB)
			})

			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID: authA, Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"priority": "100", "base_url": server.URL, "api_key": "account-a"},
				Metadata:   map[string]any{"disable_cooling": true},
			}); errRegister != nil {
				t.Fatal(errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID: authB, Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"priority": "0", "base_url": server.URL, "api_key": "account-b"},
				Metadata:   map[string]any{"disable_cooling": true},
			}); errRegister != nil {
				t.Fatal(errRegister)
			}

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)),
			}
			opts := cliproxyexecutor.Options{
				Stream:          true,
				SourceFormat:    sdktranslator.FromString("openai-response"),
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: req.Payload,
			}

			streamResult, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			if errStream != nil {
				t.Fatalf("unexpected ExecuteStream start error: %v", errStream)
			}

			var streamBody string
			var chunkErr error
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
				streamBody += string(chunk.Payload)
			}

			// Partial output MUST be delivered to downstream
			if !strings.Contains(streamBody, "MOCK_A_PARTIAL") {
				t.Fatalf("expected partial output MOCK_A_PARTIAL in stream body, got: %s", streamBody)
			}
			// Incomplete stream error MUST be delivered at the end
			if chunkErr == nil {
				t.Fatal("expected incomplete stream terminal chunk error, got nil")
			}

			mu.Lock()
			gotAttempts := append([]string(nil), attempts...)
			mu.Unlock()

			// Must ONLY attempt account-a, NOT fail over to account-b
			if len(gotAttempts) != 1 || gotAttempts[0] != "account-a" {
				t.Fatalf("attempts = %v, want [account-a] (partial output must not replay to B)", gotAttempts)
			}
		})
	}
}

// TestCodexStreamAbnormalDisconnectBeforeOutputFailsOver tests that abnormal connection
// termination (e.g. TCP reset / connection close via Hijacker) before output also fails over.
func TestCodexStreamAbnormalDisconnectBeforeOutputFailsOver(t *testing.T) {
	const model = "gpt-6-astra"
	var mu sync.Mutex
	var attempts []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		attempts = append(attempts, account)
		mu.Unlock()

		if account == "account-a" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			// Hijack and close TCP connection immediately to simulate abnormal drop.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, errHijack := hj.Hijack(); errHijack == nil {
					_ = conn.Close()
					return
				}
			}
			return
		}

		// Account B completes the stream
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_b\",\"model\":%q}}\n\n", model)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_b\",\"status\":\"completed\",\"output\":[]}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 2)
	cfg := &config.Config{}
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))

	const authA = "codex-abnormal-a"
	const authB = "codex-abnormal-b"
	registry.GetGlobalRegistry().RegisterClient(authA, "codex", []*registry.ModelInfo{{ID: model}})
	registry.GetGlobalRegistry().RegisterClient(authB, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA)
		registry.GetGlobalRegistry().UnregisterClient(authB)
	})

	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authA, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"priority": "100", "base_url": server.URL, "api_key": "account-a"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authB, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"priority": "0", "base_url": server.URL, "api_key": "account-b"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}

	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)),
	}
	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: req.Payload,
	}

	streamResult, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream failed instead of failing over: %v", errStream)
	}
	if streamResult == nil {
		t.Fatal("expected non-nil streamResult after failover")
	}

	var streamBody string
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		streamBody += string(chunk.Payload)
	}

	if !strings.Contains(streamBody, "response.completed") {
		t.Fatalf("stream body missing expected completion from B: %s", streamBody)
	}

	mu.Lock()
	gotAttempts := append([]string(nil), attempts...)
	mu.Unlock()

	if len(gotAttempts) != 2 || gotAttempts[0] != "account-a" || gotAttempts[1] != "account-b" {
		t.Fatalf("attempts = %v, want [account-a, account-b]", gotAttempts)
	}
}
