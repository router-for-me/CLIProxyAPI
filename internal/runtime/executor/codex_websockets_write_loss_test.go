package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A reused upstream connection whose write fails is invalidated before any reader sees the
// failure. The loss must still be recorded, so the next continuation is answered as "resend the
// full input" instead of requiring a replay that closes the downstream socket.
func TestCodexWebsocketWriteLossIsRecordedForNextContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Logf("close upstream: %v", errClose)
			}
		}()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/responses"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatal(errDial)
	}

	const sessionID = "write-loss-session"
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	sess := exec.getOrCreateSession(sessionID)
	sess.connMu.Lock()
	sess.conn = conn
	sess.connCloser = newWebsocketConnectionCloser(conn)
	sess.authID = "auth-a"
	sess.wsURL = wsURL
	sess.readerConn = conn // no reader: only the write path can see the failure
	sess.connMu.Unlock()
	// The connection is dead but nothing has marked it: the next write fails.
	if errClose := conn.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	auth := &cliproxyauth.Auth{ID: "auth-a", Provider: "codex", Attributes: map[string]string{"api_key": "k", "base_url": server.URL}}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	_, errCreate := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hi"}]}`),
	}, opts)
	if !cliproxyexecutor.IsUpstreamWebsocketTurnError(errCreate) {
		t.Fatalf("create error = %v, want upstream websocket turn error", errCreate)
	}

	_, errContinue := exec.ExecuteStream(cliproxyexecutor.WithRequiredUpstreamWebsocket(ctx), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","previous_response_id":"r1","input":[{"type":"message","role":"user","content":"hi"}]}`),
	}, opts)
	var turnErr *cliproxyexecutor.UpstreamWebsocketTurnError
	if !errors.As(errContinue, &turnErr) || !turnErr.FullContextRequired || turnErr.StatusCode() != http.StatusConflict {
		t.Fatalf("continuation error = %v, want full context required with status 409", errContinue)
	}
}
