package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type sameUpstreamRetryLogHook struct {
	session         string
	entered, resume chan struct{}
	once            sync.Once
}

func (*sameUpstreamRetryLogHook) Levels() []log.Level { return []log.Level{log.InfoLevel} }
func (h *sameUpstreamRetryLogHook) Fire(e *log.Entry) error {
	if strings.Contains(e.Message, "session="+h.session+" ") && strings.Contains(e.Message, "reason=upstream_disconnected") {
		h.once.Do(func() { close(h.entered); <-h.resume })
	}
	return nil
}

type sameUpstreamTrackedFailureHandler struct {
	base       cliproxyexecutor.UpstreamFailureHandler
	registered chan bool
	once       *sync.Once
}

func (h *sameUpstreamTrackedFailureHandler) Defer(err error, finish func()) bool {
	result := h.base.Defer(err, finish)
	h.once.Do(func() { h.registered <- result })
	return result
}
func (h *sameUpstreamTrackedFailureHandler) Recovered() { h.base.Recovered() }
func (h *sameUpstreamTrackedFailureHandler) Commit()    { h.base.Commit() }

type sameUpstreamTrackedExecutor struct {
	cliproxyauth.ProviderExecutor
	registered chan bool
	once       sync.Once
}

func (e *sameUpstreamTrackedExecutor) ForAPIKey() cliproxyauth.ProviderExecutor { return e }
func (e *sameUpstreamTrackedExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if opts.UpstreamFailureHandler != nil {
		opts.UpstreamFailureHandler = &sameUpstreamTrackedFailureHandler{base: opts.UpstreamFailureHandler, registered: e.registered, once: &e.once}
	}
	return e.ProviderExecutor.ExecuteStream(ctx, auth, req, opts)
}

func sameUpstreamWebsocketExecutor(cfg *config.Config, provider string) (cliproxyauth.ProviderExecutor, func(string) <-chan error) {
	if provider == "codex" {
		executor := NewCodexWebsocketsExecutor(cfg)
		executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		return executor, executor.UpstreamDisconnectChan
	}
	executor := NewXAIWebsocketsExecutor(cfg)
	executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	return executor, executor.UpstreamDisconnectChan
}

func TestHomeSameUpstreamRetryRegistersFailureBeforeDelivery(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		t.Run(provider, func(t *testing.T) {
			resume, entered, registered := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
			var resumeOnce sync.Once
			release := func() { resumeOnce.Do(func() { close(resume) }) }

			oldHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
			defer log.StandardLogger().ReplaceHooks(oldHooks)
			log.AddHook(&sameUpstreamRetryLogHook{session: t.Name(), entered: entered, resume: resume})
			var attempts atomic.Int32
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
				attempt := attempts.Add(1)
				if attempt == 1 {
					return
				}
				for _, frame := range []string{codexCreatedEvent, codexInProgressEvent, codexCompletedEventBody} {
					if conn.WriteMessage(websocket.TextMessage, []byte(frame)) != nil {
						return
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			cfg := codexBufferingConfig(false)
			cfg.SameUpstreamRetry = 1
			cfg.Home = config.HomeConfig{Enabled: true}
			executor, disconnectChannel := sameUpstreamWebsocketExecutor(cfg, provider)
			tracked := &sameUpstreamTrackedExecutor{ProviderExecutor: executor, registered: registered}
			auth := codexTestAuth(server.URL)
			auth.ID, auth.Provider = "same-upstream-late-registration", provider
			auth.Attributes["websockets"] = "true"
			dispatcher := &sameUpstreamHomeDispatcher{auth: auth}
			scopes := executionregistry.New()
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.PublishHomeDispatch(dispatcher, scopes, 1)
			manager.RegisterExecutor(tracked)
			defer manager.CloseExecutionSession(t.Name())
			defer release()
			req, opts := codexWebsocketRequest()
			opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}
			ctx, cancel := context.WithTimeout(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				result, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
				if result != nil {
					_, errDrain := drainChunks(result)
					if errStream == nil {
						errStream = errDrain
					}
				}
				done <- errStream
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("failure did not reach the log gate")
			}
			select {
			case deferred := <-registered:
				if !deferred {
					t.Fatal("failure consumed its retry budget before registration")
				}
			default:
				t.Fatal("connection became replaceable before failure registration")
			}
			if attempts.Load() != 1 {
				t.Fatal("failure reached the manager before registration and cleanup completed")
			}
			release()
			var err error
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("retry did not complete")
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("attempts=%d dispatches=%d error=%v", attempts.Load(), dispatcher.calls.Load(), err)
			select {
			case disconnectErr := <-disconnectChannel(t.Name()):
				t.Fatalf("recovered connection was terminated by old failure: %v", disconnectErr)
			default:
			}
			if attempts.Load() != 2 {
				t.Fatalf("attempts=%d, want 2", attempts.Load())
			}
			if dispatcher.calls.Load() != 1 {
				t.Fatalf("recovered request used %d Home scopes", dispatcher.calls.Load())
			}

		})
	}
}

type sameUpstreamPreBindCloseHook struct {
	session                         string
	connected, disconnected         chan struct{}
	connectedOnce, disconnectedOnce sync.Once
}

func (*sameUpstreamPreBindCloseHook) Levels() []log.Level { return []log.Level{log.InfoLevel} }
func (h *sameUpstreamPreBindCloseHook) Fire(e *log.Entry) error {
	if !strings.Contains(e.Message, "session="+h.session+" ") {
		return nil
	}
	if strings.Contains(e.Message, "upstream connected") {
		h.connectedOnce.Do(func() { close(h.connected); <-h.disconnected })
	}
	if strings.Contains(e.Message, "reason=upstream_disconnected") {
		h.disconnectedOnce.Do(func() { close(h.disconnected) })
	}
	return nil
}
func TestHomeSameUpstreamRetryHandlesCloseBeforeBind(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		t.Run(provider, func(t *testing.T) {
			hook := &sameUpstreamPreBindCloseHook{session: t.Name(), connected: make(chan struct{}), disconnected: make(chan struct{})}
			oldHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
			defer log.StandardLogger().ReplaceHooks(oldHooks)
			log.AddHook(hook)
			var connections, attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if connections.Add(1) == 1 {
					<-hook.connected
					return
				}
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
				attempts.Add(1)
				for _, frame := range []string{codexCreatedEvent, codexInProgressEvent, codexCompletedEventBody} {
					if conn.WriteMessage(websocket.TextMessage, []byte(frame)) != nil {
						return
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			cfg := codexBufferingConfig(true)
			cfg.SameUpstreamRetry = 2
			cfg.Home = config.HomeConfig{Enabled: true}
			executor, disconnectChannel := sameUpstreamWebsocketExecutor(cfg, provider)
			auth := codexTestAuth(server.URL)
			auth.ID, auth.Provider = "same-upstream-pre-bind-close", provider
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
			ctx, cancel := context.WithTimeout(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), 5*time.Second)
			defer cancel()
			result, err := manager.ExecuteStream(ctx, []string{provider}, req, opts)
			if result != nil {
				_, _ = drainChunks(result)
			}
			t.Logf("connections=%d attempts=%d dispatches=%d error=%v", connections.Load(), attempts.Load(), dispatcher.calls.Load(), err)
			select {
			case disconnectErr := <-disconnectChannel(t.Name()):
				t.Fatalf("disconnect notified before handler was bound: %v", disconnectErr)
			default:
			}
			if err != nil || connections.Load() != 2 || dispatcher.calls.Load() != 1 {
				t.Fatalf("did not recover the first close with the same Home scope")
			}

		})
	}
}
