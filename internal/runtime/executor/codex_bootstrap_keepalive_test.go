package executor

import (
	"context"
	"net/http"
	"testing"
)

// Live Astra overloads arrive after one or more keepalive events. Even a long
// run of heartbeats must not commit the stream before a generated event arrives.
func TestCodexBootstrapKeepaliveOverload(t *testing.T) {
	frames := []string{codexCreatedEvent, codexInProgressEvent}
	for range 32 {
		frames = append(frames, `{"type":"keepalive","sequence_number":3}`)
	}
	frames = append(frames, codexOverloadEvent)

	t.Run("sse", func(t *testing.T) {
		server := codexSSEServer(frames...)
		defer server.Close()
		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if result != nil {
			payload, streamErr := drainChunks(result)
			t.Fatalf("overload escaped bootstrap: payload bytes=%d, stream error=%v", len(payload), streamErr)
		}
		if err == nil || statusCodeFromTestError(t, err) != http.StatusServiceUnavailable {
			t.Fatalf("expected retryable bootstrap overload, got %v", err)
		}
	})

	t.Run("websocket", func(t *testing.T) {
		server := codexWebsocketServer(t, frames...)
		defer server.Close()
		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if result != nil {
			payload, streamErr := drainChunks(result)
			t.Fatalf("overload escaped bootstrap: payload bytes=%d, stream error=%v", len(payload), streamErr)
		}
		if err == nil || statusCodeFromTestError(t, err) != http.StatusServiceUnavailable {
			t.Fatalf("expected retryable bootstrap overload, got %v", err)
		}
	})

	t.Run("websocket_session_survives", func(t *testing.T) {
		notified, err := executeWebsocketStreamInSession(t, frames...)
		if err == nil || statusCodeFromTestError(t, err) != http.StatusServiceUnavailable {
			t.Fatalf("expected retryable bootstrap overload, got %v", err)
		}
		if notified {
			t.Fatal("bootstrap overload closed the downstream session before account failover")
		}
	})
}

func TestCodexBootstrapKeepaliveAfterOutputDoesNotRetry(t *testing.T) {
	frames := []string{codexCreatedEvent, codexOutputAddedEvent, `{"type":"keepalive"}`, codexOverloadEvent}
	t.Run("sse", func(t *testing.T) {
		server := codexSSEServer(frames...)
		defer server.Close()
		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil || result == nil {
			t.Fatalf("generated output must commit the stream: %v", err)
		}
		if _, streamErr := drainChunks(result); streamErr == nil {
			t.Fatal("expected the later overload to remain an in-stream failure")
		}
	})
	t.Run("websocket", func(t *testing.T) {
		server := codexWebsocketServer(t, frames...)
		defer server.Close()
		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil || result == nil {
			t.Fatalf("generated output must commit the stream: %v", err)
		}
		if _, streamErr := drainChunks(result); streamErr == nil {
			t.Fatal("expected the later overload to remain an in-stream failure")
		}
	})
}
