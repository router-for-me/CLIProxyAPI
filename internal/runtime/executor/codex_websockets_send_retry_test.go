package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketsExecuteStreamRetriesCloseSentFreshSend(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errCh := make(chan error, 4)
	payloadCh := make(chan []byte, 1)
	var websocketConnections atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected non-websocket request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade websocket: %w", err)
			return
		}
		defer func() { _ = conn.Close() }()

		connectionIndex := websocketConnections.Add(1)
		switch connectionIndex {
		case 1:
			_, _, _ = conn.ReadMessage()
		case 2:
			closePayload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "retry closed")
			if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
				errCh <- fmt.Errorf("write retry close: %w", errWrite)
			}
			time.Sleep(200 * time.Millisecond)
		case 3:
			msgType, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				errCh <- fmt.Errorf("read recovered websocket message: %w", errRead)
				return
			}
			if msgType != websocket.TextMessage {
				errCh <- fmt.Errorf("recovered websocket message type = %d, want text", msgType)
				return
			}
			payloadCh <- bytes.Clone(payload)
			completed := []byte(`{"type":"response.completed","response":{"id":"resp-recovered","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				errCh <- fmt.Errorf("write recovered completed: %w", errWrite)
			}
		default:
			errCh <- fmt.Errorf("unexpected websocket connection %d", connectionIndex)
		}
	}))
	defer server.Close()

	wsURL, err := buildCodexResponsesWebsocketURL(strings.TrimSuffix(server.URL, "/") + "/responses")
	if err != nil {
		t.Fatalf("build websocket URL: %v", err)
	}
	staleConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	closeHTTPResponseBody(resp, "test close handshake response body")
	if err != nil {
		t.Fatalf("dial stale websocket: %v", err)
	}
	if errClose := staleConn.Close(); errClose != nil {
		t.Fatalf("close stale websocket: %v", errClose)
	}

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-close-sent-send-retry"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	sess := exec.getOrCreateSession(sessionID)
	sess.connMu.Lock()
	sess.conn = staleConn
	sess.readerConn = staleConn
	sess.wsURL = wsURL
	sess.authID = "test-auth"
	sess.connMu.Unlock()

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
	assertCodexWebsocketCompletedChunk(t, result, "resp-recovered")

	select {
	case payload := <-payloadCh:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("recovered upstream type = %s, want response.create: %s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for recovered upstream payload")
	}
	if got := websocketConnections.Load(); got != 3 {
		t.Fatalf("websocket connection count = %d, want 3", got)
	}
	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled during close-sent send retry: err=%v ok=%v", errDisconnect, ok)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}

func TestShouldRetryCodexWebsocketSendError(t *testing.T) {
	fallbackableBody := []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`)
	if shouldRetryCodexWebsocketSendError(nil, nil, websocket.ErrCloseSent, fallbackableBody, 0) {
		t.Fatal("fallbackable close-sent send error should not retry before HTTP fallback")
	}
	incrementalBody := []byte(`{"type":"response.create","model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"next"}]}`)
	if !shouldRetryCodexWebsocketSendError(nil, nil, websocket.ErrCloseSent, incrementalBody, 0) {
		t.Fatal("incremental close-sent send error should be retryable")
	}
	if shouldRetryCodexWebsocketSendError(nil, nil, websocket.ErrCloseSent, incrementalBody, codexResponsesWebsocketSendRetryLimit) {
		t.Fatal("close-sent send error should stop retrying after the limit")
	}
	if shouldRetryCodexWebsocketSendError(nil, nil, fmt.Errorf("other error"), incrementalBody, 0) {
		t.Fatal("non close-sent send error should not be retryable")
	}
}
