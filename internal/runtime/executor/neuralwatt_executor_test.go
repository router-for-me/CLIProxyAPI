package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestNeuralwattExecutorIdentifier(t *testing.T) {
	e := NewNeuralwattExecutor(nil)
	if got := e.Identifier(); got != "neuralwatt" {
		t.Fatalf("Identifier() = %q, want %q", got, "neuralwatt")
	}
}

func TestNeuralwattExecutorNilCompatIsSafe(t *testing.T) {
	var e *NeuralwattExecutor
	if _, err := e.Execute(context.Background(), nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("Execute on nil executor: want error, got nil")
	}
}

// recordingSink implements OpenAICompatResponseSink and records the sequence
// of invocations so tests can assert the compat executor's hook contract.
// CaptureStreamChunk runs on the streaming goroutine, so access is mutex-guarded.
type recordingSink struct {
	mu     sync.Mutex
	events []sinkEvent
}

type sinkEvent struct {
	method  string // "response", "headers", or "chunk"
	headers http.Header
	body    []byte
}

func (s *recordingSink) CaptureResponse(_ context.Context, headers http.Header, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, sinkEvent{method: "response", headers: headers.Clone(), body: append([]byte(nil), body...)})
}

func (s *recordingSink) CaptureStreamHeaders(_ context.Context, headers http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, sinkEvent{method: "headers", headers: headers.Clone()})
}

func (s *recordingSink) CaptureStreamChunk(_ context.Context, chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, sinkEvent{method: "chunk", body: append([]byte(nil), chunk...)})
}

func (s *recordingSink) snapshot() []sinkEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sinkEvent, len(s.events))
	copy(out, s.events)
	return out
}

func newRecordingOpenAICompatExecutor(t *testing.T, serverURL string) (*OpenAICompatExecutor, *cliproxyauth.Auth, *recordingSink) {
	t.Helper()
	sink := &recordingSink{}
	executor := NewOpenAICompatExecutor("neuralwatt", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	executor.SetResponseSink(sink)
	auth := &cliproxyauth.Auth{
		Provider: "neuralwatt",
		Attributes: map[string]string{
			"base_url":     serverURL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	return executor, auth, sink
}

// TestOpenAICompatResponseSinkCaptureResponse pins the non-streaming hook:
// CaptureResponse fires exactly once, with the full upstream body and headers,
// on the success path.
func TestOpenAICompatResponseSinkCaptureResponse(t *testing.T) {
	const body = `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-neuralwatt-cost", "0.001")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	executor, auth, sink := newRecordingOpenAICompatExecutor(t, server.URL)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(`{"model":"neuralwatt-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	events := sink.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d sink events, want 1: %+v", len(events), events)
	}
	if events[0].method != "response" {
		t.Fatalf("event method = %q, want %q", events[0].method, "response")
	}
	if string(events[0].body) != body {
		t.Fatalf("captured body = %q, want %q", string(events[0].body), body)
	}
	if got := events[0].headers.Get("x-neuralwatt-cost"); got != "0.001" {
		t.Fatalf("captured cost header = %q, want %q", got, "0.001")
	}
}

// TestOpenAICompatResponseSinkCaptureStream pins the streaming hooks:
// CaptureStreamHeaders fires exactly once and before any CaptureStreamChunk,
// and CaptureStreamChunk receives each raw scanner line in arrival order.
func TestOpenAICompatResponseSinkCaptureStream(t *testing.T) {
	const dataLine = `data: {"id":"chatcmpl_1","choices":[{"delta":{"content":"hi"}}]}`
	const doneLine = `data: [DONE]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest server does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-neuralwatt-cost", "0.002")
		_, _ = w.Write([]byte(dataLine + "\n\n"))
		_, _ = w.Write([]byte(doneLine + "\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	executor, auth, sink := newRecordingOpenAICompatExecutor(t, server.URL)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(`{"model":"neuralwatt-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	// Drain the channel so the streaming goroutine finishes and every
	// CaptureStreamChunk call has landed before we inspect the sink.
	for range result.Chunks {
	}

	events := sink.snapshot()
	if len(events) == 0 {
		t.Fatal("no sink events recorded")
	}
	if events[0].method != "headers" {
		t.Fatalf("first event method = %q, want %q", events[0].method, "headers")
	}
	if got := events[0].headers.Get("x-neuralwatt-cost"); got != "0.002" {
		t.Fatalf("captured cost header = %q, want %q", got, "0.002")
	}

	// Every subsequent event must be a chunk, with the two SSE data lines
	// appearing in order (blank separator lines are also delivered as raw
	// chunks, so filter to the meaningful content).
	var lines []string
	for i, ev := range events[1:] {
		if ev.method != "chunk" {
			t.Fatalf("event %d method = %q, want %q", i+1, ev.method, "chunk")
		}
		if len(ev.body) > 0 {
			lines = append(lines, string(ev.body))
		}
	}
	if len(lines) != 2 || lines[0] != dataLine || lines[1] != doneLine {
		t.Fatalf("captured stream lines = %q, want [%q %q]", lines, dataLine, doneLine)
	}
}

// TestOpenAICompatResponseSinkNoCaptureOnError pins the success-path-only
// contract: a non-2xx upstream response must not invoke any sink hook.
func TestOpenAICompatResponseSinkNoCaptureOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer server.Close()

	executor, auth, sink := newRecordingOpenAICompatExecutor(t, server.URL)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(`{"model":"neuralwatt-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if events := sink.snapshot(); len(events) != 0 {
		t.Fatalf("sink fired on error path: %+v", events)
	}
}
