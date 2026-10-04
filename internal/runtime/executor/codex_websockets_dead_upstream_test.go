package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestCodexWebsocketsExecuteFailsFastWhenUpstreamClosesBeforeActivation replays the
// issue race where the upstream completes the websocket handshake and immediately
// closes the connection. The upstream read loop then exits while no request is
// activated yet, and a request that activates afterwards must fail fast with the
// upstream error instead of blocking forever in readCodexWebsocketMessage.
func TestCodexWebsocketsExecuteFailsFastWhenUpstreamClosesBeforeActivation(t *testing.T) {
	for _, mode := range []struct {
		name  string
		close func(conn *websocket.Conn)
	}{
		{"tcp_reset", func(conn *websocket.Conn) {
			// Abrupt TCP teardown without a websocket close frame.
			_ = conn.UnderlyingConn().Close()
		}},
		{"close_frame", func(conn *websocket.Conn) {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "gone"), time.Now().Add(time.Second))
			_ = conn.Close()
		}},
		{"immediate_close", func(conn *websocket.Conn) {
			_ = conn.Close()
		}},
		{"close_after_read", func(conn *websocket.Conn) {
			// Control: the request payload is consumed first, so the read loop is
			// still live when the request activates and must deliver the error.
			_, _, _ = conn.ReadMessage()
			_ = conn.Close()
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				mode.close(conn)
			}))
			defer upstream.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{})
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-test", "base_url": upstream.URL}}
			req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

			// The production hang happens on a background context: neither the context
			// nor the read channel ever fires. Bound the probe at 5s so a hang surfaces
			// as context.DeadlineExceeded instead of a stuck test.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			type outcome struct {
				elapsed time.Duration
				err     error
			}
			done := make(chan outcome, 1)
			start := time.Now()
			go func() {
				_, errExecute := exec.Execute(ctx, auth, req, opts)
				done <- outcome{time.Since(start), errExecute}
			}()
			select {
			case got := <-done:
				if got.err == nil {
					t.Fatal("Execute() returned without an upstream response")
				}
				if errors.Is(got.err, context.DeadlineExceeded) || errors.Is(got.err, context.Canceled) {
					t.Fatalf("request blocked until context deadline (would hang forever on a background context): %v after %v", got.err, got.elapsed)
				}
				if got.elapsed > 4*time.Second {
					t.Fatalf("request failed only after %v, which is hang-shaped: %v", got.elapsed, got.err)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("request never returned")
			}
		})
	}
}
