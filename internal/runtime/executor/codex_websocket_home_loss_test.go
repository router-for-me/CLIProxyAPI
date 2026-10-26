package executor

import (
	"bytes"
	"context"
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
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
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

// homeLossFixture runs the real handler and a Home-dispatched Codex websocket auth against an
// upstream that serves one turn per connection and drops the connection after response.completed.
type homeLossFixture struct {
	dials, creates atomic.Int32
	dispatcher     *accountedWebsocketHomeDispatcher
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
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		dial := f.dials.Add(1)
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
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
			if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
				t.Error(err)
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
	f.dispatcher = &accountedWebsocketHomeDispatcher{provider: "codex", baseURL: upstream.URL}
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

	client, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if response != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	f.client = client
	return f
}

func (f *homeLossFixture) send(t *testing.T, raw string) {
	t.Helper()
	if err := f.client.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func (f *homeLossFixture) read(t *testing.T, kind string) []byte {
	t.Helper()
	_, raw, err := f.client.ReadMessage()
	if err != nil {
		t.Fatalf("downstream closed waiting for %s: %v", kind, err)
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
	if err := f.registry.Drain(drainCtx); err != nil {
		t.Fatal(err)
	}
	if f.dispatcher.releases.Load() != f.dispatcher.calls.Load() {
		t.Fatalf("releases=%d dispatches=%d", f.dispatcher.releases.Load(), f.dispatcher.calls.Load())
	}
}

// An upstream EOF after response.completed releases the Home selection while the turn's result is
// still being delivered. That release must not cancel the delivering attempt, which made the
// conductor replay the finished create on a new connection.
func TestCodexHomeUpstreamLossDoesNotReplay(t *testing.T) {
	f := newHomeLossFixture(t)
	f.holdUntilReleased(t)
	if f.dispatcher.calls.Load() != 1 || f.creates.Load() != 1 || f.dials.Load() != 1 {
		t.Fatalf("finished create was replayed: dispatch=%d creates=%d dials=%d", f.dispatcher.calls.Load(), f.creates.Load(), f.dials.Load())
	}
	f.finish(t)
}
