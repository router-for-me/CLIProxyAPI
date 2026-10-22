package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type sameUpstreamHomeDispatcher struct {
	auth  *cliproxyauth.Auth
	calls atomic.Int32
}

func (*sameUpstreamHomeDispatcher) HeartbeatOK() bool       { return true }
func (*sameUpstreamHomeDispatcher) AbortAmbiguousDispatch() {}
func (d *sameUpstreamHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(map[string]any{"auth": d.auth})
}

func TestHomeCodexWebsocketSameUpstreamRetry(t *testing.T) {
	for _, failure := range []string{"overload", "disconnect"} {
		t.Run(failure, func(t *testing.T) {
			var attempts, connections atomic.Int32
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				connections.Add(1)
				for {
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					attempt := attempts.Add(1)
					if attempt < 3 && failure == "disconnect" {
						return
					}
					frames := []string{codexCreatedEvent, codexInProgressEvent, codexCompletedEventBody}
					if attempt < 3 {
						frames[2] = codexOverloadEvent
					}
					for _, frame := range frames {
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
							t.Errorf("frame write: %v", errWrite)
							return
						}
					}
				}
			}))
			defer server.Close()
			cfg := codexBufferingConfig(true)
			cfg.SameUpstreamRetry = 2
			cfg.Home = config.HomeConfig{Enabled: true}
			executor := NewCodexWebsocketsExecutor(cfg)
			executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := codexTestAuth(server.URL)
			auth.ID, auth.Provider = "same-upstream-home-codex", "codex"
			auth.Attributes["websockets"] = "true"
			dispatcher := &sameUpstreamHomeDispatcher{auth: auth}
			scopes := executionregistry.New()
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.PublishHomeDispatch(dispatcher, scopes, 1)
			manager.RegisterExecutor(executor)
			defer manager.CloseExecutionSession(t.Name())
			req, opts := codexWebsocketRequest()
			opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}
			ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
			result, errStream := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
			if errStream != nil {
				t.Fatal(errStream)
			}
			if _, errDrain := drainChunks(result); errDrain != nil {
				t.Fatal(errDrain)
			}
			if attempts.Load() != 3 || dispatcher.calls.Load() != 1 {
				t.Fatalf("attempts=%d dispatches=%d, want 3 attempts in one Home scope", attempts.Load(), dispatcher.calls.Load())
			}
			result, errStream = manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
			if errStream != nil {
				t.Fatal(errStream)
			}
			if _, errDrain := drainChunks(result); errDrain != nil {
				t.Fatal(errDrain)
			}
			if attempts.Load() != 4 || connections.Load() != 3 || dispatcher.calls.Load() != 1 {
				t.Fatalf("next request: attempts=%d connections=%d dispatches=%d, want to reuse the recovered connection and scope", attempts.Load(), connections.Load(), dispatcher.calls.Load())
			}
			manager.CloseExecutionSession(t.Name())
			if errDrain := scopes.Drain(context.Background()); errDrain != nil {
				t.Fatal(errDrain)
			}
		})
	}
}

func TestHomeCodexSameUpstreamRetryReleasesTerminalScope(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		frames := []string{codexCreatedEvent, codexInProgressEvent, `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"type":"server_error","status":500,"message":"upstream processing failed"}}}`}
		for _, frame := range frames {
			if err = conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	cfg := codexBufferingConfig(true)
	cfg.SameUpstreamRetry = 2
	cfg.Home = config.HomeConfig{Enabled: true}
	exec := NewCodexWebsocketsExecutor(cfg)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := codexTestAuth(server.URL)
	auth.ID, auth.Provider = "same-upstream-home-terminal", "codex"
	auth.Attributes["websockets"] = "true"
	dispatcher := &sameUpstreamHomeDispatcher{auth: auth}
	scopes := executionregistry.New()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.PublishHomeDispatch(dispatcher, scopes, 1)
	manager.RegisterExecutor(exec)
	defer manager.CloseExecutionSession(t.Name())
	req, opts := codexWebsocketRequest()
	opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	result, err := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if result != nil {
		_, _ = drainChunks(result)
	}
	sess := exec.getOrCreateSession(t.Name())
	sess.connMu.Lock()
	conn := sess.conn
	sess.connMu.Unlock()
	active := scopes.FreezeInFlight(time.Time{}).Executions
	select {
	case <-exec.UpstreamDisconnectChan(t.Name()):
	default:
		t.Fatal("terminal failure did not notify the downstream session")
	}
	if conn != nil || len(active) != 0 {
		t.Fatalf("connection present=%t active Home scopes=%d, want the failed connection and scope released", conn != nil, len(active))
	}
}

func TestHomeCodexUnbufferedErrorSameUpstreamRetry(t *testing.T) {
	for _, failure := range []struct {
		name, frame string
	}{
		{"error", `{"type":"error","status":503,"error":{"type":"server_error","message":"upstream overloaded"}}`},
		{"response_failed", `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"type":"server_error","status":500,"message":"upstream processing failed"}}}`},
	} {
		for _, mode := range []string{"recovered", "exhausted", "after_payload"} {
			t.Run(failure.name+"/"+mode, func(t *testing.T) {
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, err = conn.ReadMessage(); err != nil {
						return
					}
					attempt := attempts.Add(1)
					frames := []string{failure.frame}
					if mode == "recovered" && attempt > 1 {
						frames = []string{codexCreatedEvent, codexCompletedEventBody}
					} else if mode == "after_payload" {
						frames = append([]string{codexCreatedEvent}, frames...)
					}
					for _, frame := range frames {
						if err = conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
				}))
				defer server.Close()
				cfg := codexBufferingConfig(false)
				cfg.SameUpstreamRetry = 1
				cfg.Home = config.HomeConfig{Enabled: true}
				exec := NewCodexWebsocketsExecutor(cfg)
				exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				auth := codexTestAuth(server.URL)
				auth.ID, auth.Provider = "same-upstream-unbuffered", "codex"
				auth.Attributes["websockets"] = "true"
				dispatcher := &sameUpstreamHomeDispatcher{auth: auth}
				scopes := executionregistry.New()
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				manager.PublishHomeDispatch(dispatcher, scopes, 1)
				manager.RegisterExecutor(exec)
				defer manager.CloseExecutionSession(t.Name())
				req, opts := codexWebsocketRequest()
				opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}
				ctx, cancel := context.WithTimeout(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), 5*time.Second)
				defer cancel()
				result, errStream := manager.ExecuteStream(ctx, []string{"codex"}, req, opts)
				if result != nil {
					_, errDrain := drainChunks(result)
					if errStream == nil {
						errStream = errDrain
					}
				}
				if mode == "recovered" {
					if errStream != nil {
						t.Fatal(errStream)
					}
					select {
					case errDisconnect := <-exec.UpstreamDisconnectChan(t.Name()):
						t.Fatalf("recovered retry notified downstream disconnect: %v", errDisconnect)
					default:
					}
				} else {
					if mode == "exhausted" && errStream == nil {
						t.Fatal("expected terminal stream error")
					}
					select {
					case <-exec.UpstreamDisconnectChan(t.Name()):
					case <-ctx.Done():
						t.Fatal("terminal stream error did not notify downstream disconnect")
					}
					if active := scopes.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
						t.Fatalf("terminal stream retained %d Home scopes", len(active))
					}
				}
				if mode == "exhausted" {
					if attempts.Load() < 2 {
						t.Fatalf("attempts=%d, want retry budget exhausted", attempts.Load())
					}
				} else {
					wantAttempts := int32(2)
					if mode == "after_payload" {
						wantAttempts = 1
					}
					if attempts.Load() != wantAttempts || dispatcher.calls.Load() != 1 {
						t.Fatalf("attempts=%d dispatches=%d, want %d attempts in one Home scope", attempts.Load(), dispatcher.calls.Load(), wantAttempts)
					}
				}
			})
		}
	}
}
