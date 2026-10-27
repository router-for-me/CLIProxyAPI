package executor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// completedWriteBarrierListener holds the first downstream frame carrying response.completed
// inside net.Conn.Write, so the handler cannot move on until proceed is closed.
type completedWriteBarrierListener struct {
	net.Listener
	armed   atomic.Bool
	held    chan struct{}
	proceed chan struct{}
}

func (l *completedWriteBarrierListener) Accept() (net.Conn, error) {
	conn, errAccept := l.Listener.Accept()
	if errAccept != nil {
		return nil, errAccept
	}
	return &completedWriteBarrierConn{Conn: conn, listener: l}, nil
}

type completedWriteBarrierConn struct {
	net.Conn
	listener *completedWriteBarrierListener
}

func (c *completedWriteBarrierConn) Write(p []byte) (int, error) {
	// Server frames are neither masked nor compressed here.
	if bytes.Contains(p, []byte(`"response.completed"`)) && c.listener.armed.CompareAndSwap(true, false) {
		close(c.listener.held)
		<-c.listener.proceed
	}
	return c.Conn.Write(p)
}

func waitHomeLossSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

type homeLossDispatcher struct {
	accountedWebsocketHomeDispatcher
	pinnedAuthID atomic.Value
}

func (d *homeLossDispatcher) RPopAuthWithConstraints(ctx context.Context, model, sessionID string, headers http.Header, authCount int, _ []string, pinnedAuthID string) ([]byte, error) {
	d.pinnedAuthID.Store(pinnedAuthID)
	return d.RPopAuth(ctx, model, sessionID, headers, authCount)
}

// homeLossFixture runs the real handler and a Home-dispatched Codex websocket auth against an
// upstream that serves one turn per connection and drops the connection after response.completed.
type homeLossFixture struct {
	dials, creates atomic.Int32
	dispatcher     *homeLossDispatcher
	manager        *cliproxyauth.Manager
	registry       *executionregistry.Registry
	barrier        *completedWriteBarrierListener
	client         *websocket.Conn
	firstReleased  chan struct{}
	handlerDone    chan struct{}
	proceeded      bool
}

func newHomeLossFixture(t *testing.T) *homeLossFixture {
	t.Helper()
	f := &homeLossFixture{firstReleased: make(chan struct{}), handlerDone: make(chan struct{})}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Logf("close upstream websocket: %v", errClose)
			}
		}()
		dial := f.dials.Add(1)
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Error(errRead)
			return
		}
		f.creates.Add(1)
		id := "old"
		if dial > 1 {
			id = "new"
		}
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"` + id + `"}}`,
			`{"type":"response.completed","response":{"id":"` + id + `","status":"completed","output":[]}}`,
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
				t.Error(errWrite)
				return
			}
		}
		if dial == 1 {
			return // EOF right after response.completed
		}
		if gjson.GetBytes(body, "previous_response_id").Exists() {
			t.Errorf("continuation reached upstream: %s", body)
		}
		_, _, _ = conn.ReadMessage()
	}))
	t.Cleanup(upstream.Close)

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}, SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
	exec := NewCodexAutoExecutor(cfg)
	exec.wsExec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	f.manager = cliproxyauth.NewManager(nil, nil, nil)
	f.manager.SetConfig(cfg)
	f.manager.SetRetryConfig(0, 0, 0)
	f.manager.RegisterExecutor(exec)
	f.registry = executionregistry.New()
	f.dispatcher = &homeLossDispatcher{accountedWebsocketHomeDispatcher: accountedWebsocketHomeDispatcher{provider: "codex", baseURL: upstream.URL}}
	f.registry.SetReleaseSink(func(executionregistry.ReleaseGroup, int64) {
		if f.dispatcher.releases.Add(1) == 1 {
			close(f.firstReleased)
		}
	})
	f.manager.PublishHomeDispatch(f.dispatcher, f.registry, 1)

	h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, f.manager))
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) { defer close(f.handlerDone); h.ResponsesWebsocket(c) })
	server := httptest.NewUnstartedServer(router)
	f.barrier = &completedWriteBarrierListener{Listener: server.Listener, held: make(chan struct{}), proceed: make(chan struct{})}
	f.barrier.armed.Store(true)
	server.Listener = f.barrier
	server.Start()
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if !f.proceeded {
			close(f.barrier.proceed)
		}
	})

	client, response, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if response != nil && response.Body != nil {
		defer func() {
			if errClose := response.Body.Close(); errClose != nil {
				t.Logf("close handshake response body: %v", errClose)
			}
		}()
	}
	if errDial != nil {
		t.Fatal(errDial)
	}
	t.Cleanup(func() {
		if errClose := client.Close(); errClose != nil {
			t.Logf("close downstream websocket: %v", errClose)
		}
	})
	if errDeadline := client.SetReadDeadline(time.Now().Add(10 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	f.client = client
	return f
}

func (f *homeLossFixture) send(t *testing.T, raw string) {
	t.Helper()
	if errWrite := f.client.WriteMessage(websocket.TextMessage, []byte(raw)); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func (f *homeLossFixture) read(t *testing.T, kind string) []byte {
	t.Helper()
	_, raw, errRead := f.client.ReadMessage()
	if errRead != nil {
		t.Fatalf("downstream closed waiting for %s: %v", kind, errRead)
	}
	if gjson.GetBytes(raw, "type").String() != kind {
		t.Fatalf("got %s, want %s", raw, kind)
	}
	return raw
}

// holdUntilReleased leaves the handler blocked on the response.completed write until the idle EOF
// has released the Home selection.
func (f *homeLossFixture) holdUntilReleased(t *testing.T) {
	t.Helper()
	f.send(t, `{"type":"response.create","model":"model-a","input":[{"role":"user","content":"original full context"}]}`)
	f.read(t, "response.created")
	waitHomeLossSignal(t, f.barrier.held, "handler held at response.completed write")
	waitHomeLossSignal(t, f.firstReleased, "Home selection release")
	f.proceeded = true
	close(f.barrier.proceed)
	f.read(t, "response.completed")
}

func (f *homeLossFixture) finish(t *testing.T) {
	t.Helper()
	_ = f.client.Close()
	waitHomeLossSignal(t, f.handlerDone, "downstream handler")
	drainCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if errDrain := f.registry.Drain(drainCtx); errDrain != nil {
		t.Fatal(errDrain)
	}
	if f.dispatcher.releases.Load() != f.dispatcher.calls.Load() {
		t.Fatalf("releases=%d dispatches=%d", f.dispatcher.releases.Load(), f.dispatcher.calls.Load())
	}
}

// An upstream EOF after response.completed releases the Home selection while the turn's result is
// still being delivered. That release must not cancel the delivering attempt, which made the
// conductor replay the finished create on a new connection, and must keep the credential pinned so
// a continuation returns 409 and a fresh full create reconnects on the same credential.
func TestCodexHomeUpstreamLossDoesNotReplay(t *testing.T) {
	f := newHomeLossFixture(t)
	f.holdUntilReleased(t)
	if f.dispatcher.calls.Load() != 1 || f.creates.Load() != 1 || f.dials.Load() != 1 {
		t.Fatalf("finished create was replayed: dispatch=%d creates=%d dials=%d", f.dispatcher.calls.Load(), f.creates.Load(), f.dials.Load())
	}

	f.send(t, `{"type":"response.create","model":"model-a","previous_response_id":"old","input":[{"role":"user","content":"follow up"}]}`)
	errFrame := f.read(t, "error")
	if got := gjson.GetBytes(errFrame, "error.code").String(); got != "previous_response_not_found" {
		t.Fatalf("continuation error = %s, want previous_response_not_found", errFrame)
	}
	if f.dispatcher.calls.Load() != 1 || f.creates.Load() != 1 || f.dials.Load() != 1 {
		t.Fatalf("refused continuation dispatched upstream: dispatch=%d creates=%d dials=%d", f.dispatcher.calls.Load(), f.creates.Load(), f.dials.Load())
	}

	var lossEpoch atomic.Uint64
	lossEpoch.Store(1)
	staleContCtx := cliproxyexecutor.WithWebsocketInputEpoch(
		cliproxyexecutor.WithRequiredUpstreamWebsocket(cliproxyexecutor.WithDownstreamWebsocket(context.Background())),
		&lossEpoch,
		0,
	)
	_, errStaleCont := f.manager.ExecuteStream(
		staleContCtx,
		[]string{"codex"},
		cliproxyexecutor.Request{Model: "model-a", Payload: []byte(`{"type":"response.create","model":"model-a","previous_response_id":"old"}`)},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "stale-home-session"}},
	)
	var turnErr *cliproxyexecutor.UpstreamWebsocketTurnError
	if !errors.As(errStaleCont, &turnErr) || !turnErr.FullContextRequired || turnErr.StatusCode() != http.StatusConflict {
		t.Fatalf("stale Home continuation error = %v, want 409 FullContextRequired", errStaleCont)
	}
	if f.dispatcher.calls.Load() != 1 {
		t.Fatalf("stale Home continuation dispatched upstream: dispatch=%d", f.dispatcher.calls.Load())
	}

	f.send(t, `{"type":"response.create","model":"model-a","input":[{"role":"user","content":"fresh full context"}]}`)
	f.read(t, "response.created")
	completed := f.read(t, "response.completed")
	if got := gjson.GetBytes(completed, "response.id").String(); got != "new" {
		t.Fatalf("reconnected response id = %q, want new (%s)", got, completed)
	}
	if got, _ := f.dispatcher.pinnedAuthID.Load().(string); got != "accounted-websocket-auth" {
		t.Fatalf("reconnect pinnedAuthID = %q, want accounted-websocket-auth", got)
	}
	if f.dispatcher.calls.Load() != 2 || f.creates.Load() != 2 || f.dials.Load() != 2 {
		t.Fatalf("reconnect counts: dispatch=%d creates=%d dials=%d, want 2/2/2", f.dispatcher.calls.Load(), f.creates.Load(), f.dials.Load())
	}
	f.finish(t)
}
