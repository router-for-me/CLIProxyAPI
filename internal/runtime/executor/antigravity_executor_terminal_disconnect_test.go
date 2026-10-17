package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestAntigravityStream_ClientDisconnectAfterTerminalChunkDoesNotReportFailure
// reproduces Issue #6376 where downstream client disconnects after terminal event,
// causing upstream scanner context cancellation before upstream EOF.
// The settlement tail must treat this as success and publish observed usage.
func TestAntigravityStream_ClientDisconnectAfterTerminalChunkDoesNotReportFailure(t *testing.T) {
	const authID = "antigravity-term-disconnect-test"
	capture := &antigravityUsageCapture{authID: authID, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
	})

	const upstreamSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello, world!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3.8-flash","responseId":"resp-123"}}

`

	upstreamClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the upstream body open until downstream disconnects or test ends,
		// reproducing upstream lag where body close / EOF happens after terminal event.
		select {
		case <-r.Context().Done():
		case <-upstreamClosed:
		}
	}))
	defer func() {
		close(upstreamClosed)
		server.Close()
	}()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})

	deliveryCtx, finishDelivery := usage.WithStreamDelivery(context.Background())
	ctx, cancel := context.WithCancel(deliveryCtx)
	defer cancel()

	result, errExecute := executor.ExecuteStream(ctx, &cliproxyauth.Auth{
		ID: authID,
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	// Consume chunks until terminal event response.completed is observed
	terminalReceived := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			break
		}
		if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
			terminalReceived = true
			// Client received completed response and terminates connection
			finishDelivery(context.Canceled)
			cancel()
			break
		}
	}

	if !terminalReceived {
		t.Fatal("expected to receive response.completed chunk before cancellation")
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed: status=%d, body=%q", record.Fail.StatusCode, record.Fail.Body)
		}
		if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 || record.Detail.TotalTokens != 15 {
			t.Fatalf("reported usage = %+v, want input 10 output 5 total 15", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

// TestAntigravityStream_ClientDisconnectPlainContextAfterTerminalChunkDoesNotReportFailure
// covers plain cancellation without streamDelivery tracked.
func TestAntigravityStream_ClientDisconnectPlainContextAfterTerminalChunkDoesNotReportFailure(t *testing.T) {
	const authID = "antigravity-plain-cancel-test"
	capture := &antigravityUsageCapture{authID: authID, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
	})

	const upstreamSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello, world!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":8,"totalTokenCount":28},"modelVersion":"gemini-3.8-flash","responseId":"resp-456"}}

`

	upstreamClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-upstreamClosed:
		}
	}))
	defer func() {
		close(upstreamClosed)
		server.Close()
	}()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, errExecute := executor.ExecuteStream(ctx, &cliproxyauth.Auth{
		ID: authID,
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	terminalReceived := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			break
		}
		if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
			terminalReceived = true
			cancel()
			break
		}
	}

	if !terminalReceived {
		t.Fatal("expected to receive response.completed chunk before cancellation")
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed: status=%d, body=%q", record.Fail.StatusCode, record.Fail.Body)
		}
		if record.Detail.InputTokens != 20 || record.Detail.OutputTokens != 8 || record.Detail.TotalTokens != 28 {
			t.Fatalf("reported usage = %+v, want input 20 output 8 total 28", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

// TestAntigravityStream_OpenAIChatCompletions_ClientDisconnectAfterStopChunkDoesNotReportFailure
// covers standard chat completions FormatOpenAI streaming where client disconnects after finish_reason.
func TestAntigravityStream_OpenAIChatCompletions_ClientDisconnectAfterStopChunkDoesNotReportFailure(t *testing.T) {
	const authID = "antigravity-chat-disconnect-test"
	capture := &antigravityUsageCapture{authID: authID, records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), antigravityUsageNoop{})
	})

	const upstreamSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello, world!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":12,"totalTokenCount":42},"modelVersion":"gemini-3.8-flash","responseId":"resp-789"}}

`

	upstreamClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-upstreamClosed:
		}
	}))
	defer func() {
		close(upstreamClosed)
		server.Close()
	}()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, errExecute := executor.ExecuteStream(ctx, &cliproxyauth.Auth{
		ID: authID,
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	terminalReceived := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			break
		}
		if gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String() != "" {
			terminalReceived = true
			cancel()
			break
		}
	}

	if !terminalReceived {
		t.Fatal("expected to receive finish_reason chunk before cancellation")
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed: status=%d, body=%q", record.Fail.StatusCode, record.Fail.Body)
		}
		if record.Detail.InputTokens != 30 || record.Detail.OutputTokens != 12 || record.Detail.TotalTokens != 42 {
			t.Fatalf("reported usage = %+v, want input 30 output 12 total 42", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}
