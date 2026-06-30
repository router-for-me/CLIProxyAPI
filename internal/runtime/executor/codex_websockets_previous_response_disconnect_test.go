package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexWebsocketShouldNotifyUpstreamDisconnectOnlySuppressesDownstreamPreviousResponseMiss(t *testing.T) {
	previousResponseErr := statusErr{
		code: http.StatusBadRequest,
		msg:  `{"status":400,"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp-1' not found.","param":"previous_response_id"}}`,
	}
	genericErr := statusErr{
		code: http.StatusInternalServerError,
		msg:  `{"status":500,"error":{"type":"server_error","code":"upstream_failed","message":"upstream failed"}}`,
	}

	if !codexWebsocketShouldNotifyUpstreamDisconnect(context.Background(), previousResponseErr) {
		t.Fatal("non-downstream previous_response_not_found should still notify disconnect subscribers")
	}
	if codexWebsocketShouldNotifyUpstreamDisconnect(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), previousResponseErr) {
		t.Fatal("downstream previous_response_not_found should not notify before transcript replay can run")
	}
	if !codexWebsocketShouldNotifyUpstreamDisconnect(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), genericErr) {
		t.Fatal("downstream generic upstream errors should still notify disconnect subscribers")
	}
}

func TestCodexWebsocketsExecuteStreamDoesNotNotifyDisconnectForReplayablePreviousResponseMiss(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errCh := make(chan error, 2)
	errorPayload := []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp-1' not found.","param":"previous_response_id"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade websocket: %w", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			errCh <- fmt.Errorf("read upstream websocket message: %w", errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, errorPayload); errWrite != nil {
			errCh <- fmt.Errorf("write previous response error: %w", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-previous-response-miss"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"next"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before previous-response error chunk")
		}
		if len(bytes.TrimSpace(chunk.Payload)) != 0 {
			t.Fatalf("error chunk payload = %s, want empty", chunk.Payload)
		}
		if chunk.Err == nil {
			t.Fatal("error chunk Err = nil, want previous_response_not_found")
		}
		statusErr, ok := chunk.Err.(interface{ StatusCode() int })
		if !ok || statusErr.StatusCode() != http.StatusBadRequest {
			t.Fatalf("chunk Err = %v, want status 400", chunk.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for previous-response error chunk")
	}

	assertNoCodexWebsocketDisconnectSignal(t, disconnectCh, "for replayable previous_response_not_found")
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}
