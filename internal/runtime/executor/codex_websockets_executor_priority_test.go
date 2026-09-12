package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// codexPriorityTransportTestServer accepts both websocket upgrades and plain
// HTTP requests on the same address and records which transport served each
// request, so tests can assert the CodexAutoExecutor dispatch decision.
type codexPriorityTransportTestServer struct {
	server     *httptest.Server
	wsBodies   chan []byte
	httpBodies chan []byte
}

func newCodexPriorityTransportTestServer(t *testing.T) *codexPriorityTransportTestServer {
	t.Helper()
	transport := &codexPriorityTransportTestServer{
		wsBodies:   make(chan []byte, 4),
		httpBodies: make(chan []byte, 4),
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	transport.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade websocket: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, body, err := conn.ReadMessage()
			if err != nil {
				t.Errorf("read websocket request: %v", err)
				return
			}
			transport.wsBodies <- bytes.Clone(body)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)); err != nil {
				t.Errorf("write websocket completion: %v", err)
			}
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read HTTP request: %v", err)
			return
		}
		transport.httpBodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	t.Cleanup(transport.server.Close)
	return transport
}

func (s *codexPriorityTransportTestServer) auth(websockets bool) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":    "test",
		"base_url":   s.server.URL,
		"websockets": strconv.FormatBool(websockets),
	}}
}

func drainCodexPriorityStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
}

func priorityStreamOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	}
}

func emptyPriorityTestConfig() *config.Config {
	return &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
}

func TestCodexAutoExecutorPriorityPayloadUsesUpstreamWebsocket(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(emptyPriorityTestConfig())
	result, err := exec.ExecuteStream(context.Background(), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"active","service_tier":"priority","previous_response_id":"resp_previous"}`),
	}, priorityStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.wsBodies:
		if got := gjson.GetBytes(body, "type").String(); got != "response.create" {
			t.Fatalf("websocket request type = %q, want response.create; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
			t.Fatalf("websocket service_tier = %q, want priority; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4" {
			t.Fatalf("websocket model = %q, want gpt-5.4; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "input.0.content.0.text").String(); got != "active" {
			t.Fatalf("websocket input = %q, want active translated payload; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "previous_response_id").String(); got != "resp_previous" {
			t.Fatalf("websocket previous_response_id = %q, want preserved ID; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket priority request")
	}
	select {
	case body := <-transport.httpBodies:
		t.Fatalf("unexpected HTTP request: %s", body)
	default:
	}
}

// This mirrors the payload-override recipe shipped in config.example.yaml
// (gpt-5.4-fast -> service_tier: priority) and verifies the resolved rule
// routes the request onto the upstream websocket transport.
func TestCodexAutoExecutorExampleFastAliasOverrideUsesUpstreamWebsocket(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Payload: config.PayloadConfig{Default: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-5.4-fast", Protocol: "codex"}},
			Params: map[string]any{"service_tier": "priority"},
		}}},
	})
	opts := priorityStreamOptions()
	opts.Metadata = map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4-fast"}
	opts.OriginalRequest = []byte(`{"model":"gpt-5.4-fast","input":"original"}`)
	result, err := exec.ExecuteStream(context.Background(), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"active"}`),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.wsBodies:
		if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
			t.Fatalf("websocket service_tier = %q, want priority; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4" {
			t.Fatalf("websocket model = %q, want resolved gpt-5.4; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket override request")
	}
	select {
	case body := <-transport.httpBodies:
		t.Fatalf("unexpected HTTP request: %s", body)
	default:
	}
}

// This mirrors Claude ingress and verifies a from-protocol: claude override is
// applied before the transport eligibility decision.
func TestCodexAutoExecutorClaudePriorityOverrideUsesUpstreamWebsocket(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{
				Name:         "gpt-5.4",
				Protocol:     "codex",
				FromProtocol: "claude",
			}},
			Params: map[string]any{"service_tier": "priority"},
		}}},
	})
	originalRequest := []byte(`{"model":"gpt-5.4","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"original"}]}`)
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: originalRequest,
		Stream:          true,
	}
	result, err := exec.ExecuteStream(context.Background(), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"active"}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.wsBodies:
		if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
			t.Fatalf("websocket service_tier = %q, want priority; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "input.0.content.0.text").String(); got != "active" {
			t.Fatalf("websocket input = %q, want active Claude message; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Claude websocket priority request")
	}
	select {
	case body := <-transport.httpBodies:
		t.Fatalf("unexpected HTTP request: %s", body)
	default:
	}
}

func TestCodexAutoExecutorStandardStreamUsesHTTP(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(emptyPriorityTestConfig())
	result, err := exec.ExecuteStream(context.Background(), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}, priorityStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.httpBodies:
		if gjson.GetBytes(body, "service_tier").Exists() {
			t.Fatalf("unexpected HTTP service_tier: %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
	}
	select {
	case body := <-transport.wsBodies:
		t.Fatalf("unexpected websocket request: %s", body)
	default:
	}
}

func TestCodexAutoExecutorPriorityWithoutWebsocketsAttributeUsesHTTP(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(emptyPriorityTestConfig())
	result, err := exec.ExecuteStream(context.Background(), transport.auth(false), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","service_tier":"priority"}`),
	}, priorityStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.httpBodies:
		if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
			t.Fatalf("HTTP service_tier = %q, want priority; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
	}
	select {
	case body := <-transport.wsBodies:
		t.Fatalf("unexpected websocket request: %s", body)
	default:
	}
}

func TestCodexAutoExecutorDownstreamWebsocketDispatchUnchanged(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(emptyPriorityTestConfig())
	result, err := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","previous_response_id":"resp_previous"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.wsBodies:
		if got := gjson.GetBytes(body, "previous_response_id").String(); got != "resp_previous" {
			t.Fatalf("downstream websocket previous_response_id = %q, want preserved ID; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for downstream websocket request")
	}
	select {
	case body := <-transport.httpBodies:
		t.Fatalf("unexpected HTTP request: %s", body)
	default:
	}
}

// A payload rule gated on reasoning.effort — materialized from a thinking
// suffix such as "gpt-5.4(high)" — must be evaluated after ApplyThinking,
// mirroring the order the HTTP and websocket executors resolve the payload.
func TestCodexAutoExecutorThinkingSuffixPriorityRuleUsesUpstreamWebsocket(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{
				Name:     "gpt-5.4",
				Protocol: "codex",
				Match:    []map[string]any{{"reasoning.effort": "high"}},
			}},
			Params: map[string]any{"service_tier": "priority"},
		}}},
	})
	result, err := exec.ExecuteStream(context.Background(), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: []byte(`{"model":"gpt-5.4(high)","input":"active"}`),
	}, priorityStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	drainCodexPriorityStream(t, result)

	select {
	case body := <-transport.wsBodies:
		if got := gjson.GetBytes(body, "service_tier").String(); got != "priority" {
			t.Fatalf("websocket service_tier = %q, want priority; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "high" {
			t.Fatalf("websocket reasoning.effort = %q, want high; body=%s", got, body)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4" {
			t.Fatalf("websocket model = %q, want gpt-5.4; body=%s", got, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for thinking-suffix websocket request")
	}
	select {
	case body := <-transport.httpBodies:
		t.Fatalf("unexpected HTTP request: %s", body)
	default:
	}
}

// OpenAI image requests have a specialized HTTP execution path and must never
// be routed onto the websocket transport, even when priority is requested.
func TestCodexPriorityWebsocketEligibleExcludesImageRequests(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"websockets": "true"}}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"a cat","service_tier":"priority"}`),
	}
	if codexPriorityWebsocketEligible(emptyPriorityTestConfig(), auth, req, opts) {
		t.Fatal("image requests must stay on the specialized HTTP path")
	}
}

// Non-streaming execution is intentionally out of scope for the priority
// websocket bridge; this locks the dispatch so the change stays stream-only.
func TestCodexAutoExecutorPriorityNonStreamExecuteUsesHTTP(t *testing.T) {
	transport := newCodexPriorityTransportTestServer(t)
	exec := NewCodexAutoExecutor(emptyPriorityTestConfig())
	_, _ = exec.Execute(context.Background(), transport.auth(true), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","service_tier":"priority"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})

	select {
	case <-transport.httpBodies:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
	}
	select {
	case body := <-transport.wsBodies:
		t.Fatalf("unexpected websocket request: %s", body)
	default:
	}
}
