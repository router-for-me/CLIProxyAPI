package executor

import (
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
)

func TestCodexWebsocketsExecuteStreamDoesNotReplayAfterUpstreamReceipt(t *testing.T) {
	tests := []struct {
		name      string
		closeCode int
	}{
		{name: "normal_close", closeCode: websocket.CloseNormalClosure},
		{name: "message_too_big", closeCode: websocket.CloseMessageTooBig},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			errCh := make(chan error, 4)
			var websocketConnections atomic.Int32
			var httpFallbackRequests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					httpFallbackRequests.Add(1)
					http.Error(w, "unexpected fallback", http.StatusInternalServerError)
					return
				}

				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					errCh <- fmt.Errorf("upgrade websocket: %w", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()

				connectionIndex := websocketConnections.Add(1)
				if connectionIndex != 1 {
					errCh <- fmt.Errorf("unexpected websocket retry connection: %d", connectionIndex)
					return
				}
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					errCh <- fmt.Errorf("read upstream websocket message: %w", errRead)
					return
				}
				created := []byte(`{"type":"response.created","response":{"id":"resp-created","model":"gpt-5-codex","output":[]}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, created); errWrite != nil {
					errCh <- fmt.Errorf("write created websocket message: %w", errWrite)
					return
				}
				closePayload := websocket.FormatCloseMessage(test.closeCode, "after created")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write close after created: %w", errWrite)
				}
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "sess-upstream-receipt-" + test.name
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: []byte(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai"),
				ResponseFormat: sdktranslator.FromString("openai"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}

			result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}
			select {
			case chunk, ok := <-result.Chunks:
				if !ok {
					t.Fatal("stream closed without upstream close error")
				}
				if chunk.Err == nil {
					t.Fatalf("stream chunk error = nil, payload=%s", chunk.Payload)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream close error")
			}
			select {
			case _, ok := <-result.Chunks:
				if ok {
					t.Fatal("unexpected extra stream chunk")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for stream close")
			}

			if got := websocketConnections.Load(); got != 1 {
				t.Fatalf("websocket connection count = %d, want 1", got)
			}
			if got := httpFallbackRequests.Load(); got != 0 {
				t.Fatalf("HTTP fallback request count = %d, want 0", got)
			}
			select {
			case errServer := <-errCh:
				t.Fatal(errServer)
			default:
			}
		})
	}
}
