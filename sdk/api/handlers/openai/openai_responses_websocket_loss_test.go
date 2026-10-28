package openai

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// lossFixture runs the real handler, manager and Codex executor against a scripted upstream.
// script is called once per upstream connection with its 1-based index.
type lossFixture struct {
	t           *testing.T
	base        *handlers.BaseAPIHandler
	client      *websocket.Conn
	connections atomic.Int32
	creates     atomic.Int32
	authIDs     sync.Map
}

// access, when given, authenticates the client with the header "Bearer key-1" on every turn.
func newLossFixture(t *testing.T, steering bool, script func(index int, c *websocket.Conn, read func() []byte), access ...sdkaccess.Provider) *lossFixture {
	t.Helper()
	f := &lossFixture{t: t}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(f.connections.Add(1))
		f.authIDs.Store(index, r.Header.Get("Authorization"))
		c, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() {
			if errClose := c.Close(); errClose != nil {
				t.Logf("close upstream websocket: %v", errClose)
			}
		}()
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		script(index, c, func() []byte {
			_, p, errRead := c.ReadMessage()
			if errRead != nil {
				return nil
			}
			if gjson.GetBytes(p, "type").String() == "response.create" {
				f.creates.Add(1)
			}
			return p
		})
	}))
	t.Cleanup(upstream.Close)
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = steering
	cfg.CodexResponseSteering = steering
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	// Two credentials with distinct keys, so a reconnect on another one is visible.
	for _, key := range []string{"key-a", "key-b"} {
		authID := "loss-" + key + strings.ReplaceAll(t.Name(), "/", "-")
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID:         authID,
			Provider:   "codex",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"api_key": key, "base_url": upstream.URL, "websockets": "true"},
			Metadata: map[string]any{
				"request_scoped_errors": []config.RequestScopedErrorRule{
					{Status: http.StatusBadGateway, Match: []string{"upstream_websocket_lost"}, Action: "continue"},
				},
			},
		}); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "loss-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	f.base = handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	h := NewOpenAIResponsesAPIHandler(f.base)
	header := http.Header{}
	if len(access) > 0 {
		h.AccessManager = sdkaccess.NewManager()
		h.AccessManager.SetProviders(access)
		header.Set("Authorization", "Bearer key-1")
	}
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	t.Cleanup(downstream.Close)
	client, resp, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", header)
	if resp != nil && resp.Body != nil {
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
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
	f.client = client
	return f
}

func (f *lossFixture) send(payload string) {
	f.t.Helper()
	_ = f.client.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if errWrite := f.client.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
		f.t.Fatal(errWrite)
	}
}

func (f *lossFixture) create(extra string) {
	f.send(fmt.Sprintf(`{"type":"response.create","model":"loss-model","input":[]%s}`, extra))
}

// next returns the next downstream frame; the socket must still be open.
func (f *lossFixture) next() []byte {
	f.t.Helper()
	_ = f.client.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, p, errRead := f.client.ReadMessage()
	if errRead != nil {
		f.t.Fatalf("downstream socket closed: %v", errRead)
	}
	return p
}

func (f *lossFixture) nextError() []byte {
	f.t.Helper()
	for {
		if p := f.next(); gjson.GetBytes(p, "type").String() == "error" {
			return p
		}
	}
}

func (f *lossFixture) awaitCompleted() {
	f.t.Helper()
	for {
		p := f.next()
		switch gjson.GetBytes(p, "type").String() {
		case "response.completed":
			return
		case "error":
			f.t.Fatalf("unexpected error frame: %s", p)
		}
	}
}

// awaitProbeCompleted sends creates until one completes. Errors are accepted only for the
// event_id of the probe just sent.
func (f *lossFixture) awaitProbeCompleted() {
	f.t.Helper()
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("probe%d", i)
		f.create(`,"event_id":"` + id + `"`)
		for {
			p := f.next()
			switch gjson.GetBytes(p, "type").String() {
			case "response.completed":
				return
			case "error":
				if gjson.GetBytes(p, "error.event_id").String() != id {
					f.t.Fatalf("unexpected error frame: %s", p)
				}
			}
			if gjson.GetBytes(p, "type").String() == "error" {
				break
			}
		}
	}
	f.t.Fatal("no probe completed")
}

const (
	lossCreated   = `{"type":"response.created","response":{"id":"r1","output":[]}}`
	lossCompleted = `{"type":"response.completed","response":{"id":"r2","output":[]}}`
)

func TestResponsesWebsocketUpstreamLossKeepsDownstreamOpen(t *testing.T) {
	f := newLossFixture(t, false, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		if index == 1 {
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
			return // EOF with the create outcome unknown
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		read()
	})
	f.create("")
	lost := f.nextError()
	if got := gjson.GetBytes(lost, "error.code").String(); got != "upstream_websocket_lost" {
		t.Fatalf("loss error = %s", lost)
	}
	// The turn is not replayed and a continuation cannot resume the lost socket. The full
	// create follows back to back: the refusal is not a loss and must not fence it.
	f.create(`,"previous_response_id":"r1"`)
	f.create("")
	if got := gjson.GetBytes(f.nextError(), "error.code").String(); got != "previous_response_not_found" {
		t.Fatalf("continuation after loss = %s", got)
	}
	f.awaitCompleted()
	if f.connections.Load() != 2 || f.creates.Load() != 2 {
		t.Fatalf("connections=%d creates=%d, want 2 and 2 (one create per connection)", f.connections.Load(), f.creates.Load())
	}
	first, _ := f.authIDs.Load(1)
	second, _ := f.authIDs.Load(2)
	if first != second {
		t.Fatalf("reconnect changed credential: %v vs %v", first, second)
	}
}

func TestResponsesWebsocketIdleUpstreamEOFIsSilent(t *testing.T) {
	closed := make(chan struct{})
	f := newLossFixture(t, false, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		if index == 1 {
			close(closed)
			return
		}
		read()
	})
	f.create("")
	f.awaitCompleted()
	<-closed
	// A read deadline would break the client conn for good, so silence is checked in order:
	// awaitCompleted fails on any error frame, and a loss frame would precede the completion.
	// The idle socket is gone: a continuation cannot resume it.
	f.create(`,"previous_response_id":"r2"`)
	if got := gjson.GetBytes(f.nextError(), "error.code").String(); got != "previous_response_not_found" {
		t.Fatalf("continuation after idle EOF = %s", got)
	}
	f.create("")
	f.awaitCompleted()
	if f.connections.Load() != 2 {
		t.Fatalf("connections=%d, want 2", f.connections.Load())
	}
}

func TestResponsesWebsocketDuplexLossReportsEveryOperation(t *testing.T) {
	steered := make(chan struct{})
	f := newLossFixture(t, true, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		if index == 1 {
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
			read() // the steer; never acknowledged
			close(steered)
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		read()
	})
	f.create(`,"event_id":"c1"`)
	f.send(`{"type":"response.steer","previous_response_id":"r1","input":"x","event_id":"s1"}`)
	<-steered
	want := []struct{ code, eventID string }{
		{"upstream_websocket_lost", "c1"},
		{"upstream_websocket_lost", "s1"},
	}
	for _, w := range want {
		p := f.nextError()
		if gjson.GetBytes(p, "error.code").String() != w.code || gjson.GetBytes(p, "error.event_id").String() != w.eventID {
			t.Fatalf("error = %s, want %s for %s", p, w.code, w.eventID)
		}
	}
	f.create(`,"event_id":"c3"`)
	f.awaitCompleted()
	if f.creates.Load() != 2 {
		t.Fatalf("creates=%d, want 2 (c1 and c3 only)", f.creates.Load())
	}
}

// runAcceptedSteer: successor means the accepted steer's response r2 ends after the steer is
// accepted; failure, when set, is an error event without a response id that ends it instead.
func runAcceptedSteer(t *testing.T, successor bool, failure string) {
	const accepted = `{"type":"response.steer.accepted","steer":{"id":"st1","previous_response_id":"r1"}}`
	steered := make(chan struct{})
	f := newLossFixture(t, true, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		if index == 2 {
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
			read()
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
		read() // the steer
		_ = c.WriteMessage(websocket.TextMessage, []byte(accepted))
		if successor {
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r2","previous_response_id":"r1","output":[]}}`))
			end := `{"type":"response.completed","response":{"id":"r2","previous_response_id":"r1","output":[]}}`
			if failure != "" {
				end = failure
			}
			_ = c.WriteMessage(websocket.TextMessage, []byte(end))
		}
		close(steered)
	})
	f.create(`,"event_id":"c1"`)
	f.send(`{"type":"response.steer","previous_response_id":"r1","input":"x","event_id":"s1"}`)
	<-steered
	if !successor {
		// The accepted steer and the create each get exactly one outcome-unknown error.
		for _, id := range []string{"c1", "s1"} {
			p := f.nextError()
			if gjson.GetBytes(p, "error.code").String() != "upstream_websocket_lost" || gjson.GetBytes(p, "error.event_id").String() != id {
				t.Fatalf("error = %s, want lost for %s", p, id)
			}
		}
	}
	// With the successor ended nothing is outstanding. A silent loss has no frame to wait
	// for, so probe until a create completes, which can only happen on the new connection
	// after the loss was handled. Any other error frame (such as a report for s1) fails the
	// test. A probe caught by the loss is reported with its own event_id, which
	// awaitProbeCompleted accepts; s1 was removed by the reader before it read the EOF.
	if failure != "" {
		f.nextError() // the successor's own failure, not a loss report
	} else if successor {
		f.awaitCompleted()
	}
	f.awaitProbeCompleted()
}

func TestResponsesWebsocketDuplexLossAcceptedSteer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		successor bool
	}{{"outcome unknown", false}, {"successor ended", true}} {
		t.Run(tc.name, func(t *testing.T) { runAcceptedSteer(t, tc.successor, "") })
	}
}

// An id-less error ends the successor; the idle EOF that follows must stay silent.
func TestResponsesWebsocketDuplexLossSuccessorFailedWithoutID(t *testing.T) {
	runAcceptedSteer(t, true, `{"type":"error","status":500,"error":{"type":"server_error","message":"boom"}}`)
}

// Without steering nothing copies the create's event_id into the loss error later.
func TestResponsesWebsocketLossCarriesEventIDWithoutSteering(t *testing.T) {
	f := newLossFixture(t, false, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
	})
	f.create(`,"event_id":"c1"`)
	p := f.nextError()
	if gjson.GetBytes(p, "error.code").String() != "upstream_websocket_lost" || gjson.GetBytes(p, "error.event_id").String() != "c1" {
		t.Fatalf("error = %s, want lost for c1", p)
	}
}

// A create written after the previous response ended has no running turn: its correlated
// error is the only report, with no extra unscoped loss frame, and an id-less second create
// must not inherit the completed first create's event_id.
func TestResponsesWebsocketDuplexLossBetweenTurnsReportsOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secondAttr string
		wantID     string
	}{
		{name: "second create with event_id", secondAttr: `,"event_id":"c2"`, wantID: "c2"},
		{name: "second create without event_id", secondAttr: "", wantID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLossFixture(t, true, func(index int, c *websocket.Conn, read func() []byte) {
				read()
				_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
				_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
				if index == 1 {
					read() // the second create, lost before response.created
					return
				}
				read()
			})
			f.create(`,"event_id":"c1"`)
			f.awaitCompleted()
			f.create(tc.secondAttr)
			p := f.nextError()
			if gjson.GetBytes(p, "error.code").String() != "upstream_websocket_lost" || gjson.GetBytes(p, "error.event_id").String() != tc.wantID {
				t.Fatalf("error = %s, want lost with event_id=%q", p, tc.wantID)
			}
			f.awaitProbeCompleted() // fails on any further error frame
		})
	}
}

// A create refused by the per-turn authorizer is answered locally. The EOF that follows is
// an idle loss: the refused create is not an unsent operation.
func TestResponsesWebsocketDuplexLossAfterRefusedCreateIsSilent(t *testing.T) {
	provider := &revocableWebsocketAccessProvider{}
	eof := make(chan struct{})
	f := newLossFixture(t, true, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		if index == 1 {
			<-eof
			return
		}
		read()
	}, provider)
	f.create("")
	f.awaitCompleted()
	provider.revoked.Store(true)
	f.create(`,"event_id":"c2"`)
	if p := f.nextError(); gjson.GetBytes(p, "status").Int() != http.StatusForbidden {
		t.Fatalf("error = %s, want 403", p)
	}
	provider.revoked.Store(false)
	close(eof)
	f.awaitProbeCompleted() // fails on a not-sent report for c2
}

type turn2BarrierModelRouterHost struct {
	calls        atomic.Int32
	turn2Reached chan struct{}
	releaseTurn2 <-chan struct{}
}

func (*turn2BarrierModelRouterHost) HasModelRouters() bool { return true }

func (h *turn2BarrierModelRouterHost) RouteModel(_ context.Context, _ pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	// Call 1 is turn 1 (c1), call 2 is turn 2 (c2) after the handler-level epoch checks.
	if h.calls.Add(1) == 2 {
		close(h.turn2Reached)
		<-h.releaseTurn2
	}
	return pluginapi.ModelRouteResponse{}, false
}

// In non-steering mode, a second response.create queued while turn 1 is active must be fenced
// with upstream_websocket_not_sent even when the idle EOF arrives after the handler's epoch check,
// and rejecting that stale input must not fence a third create sent after the EOF.
func TestResponsesWebsocketQueuedInputBeforeIdleEOFIsNotSent(t *testing.T) {
	turn2Reached := make(chan struct{})
	eofProcessed := make(chan struct{})
	releaseTurn2 := make(chan struct{})
	f := newLossFixture(t, false, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		if index == 1 {
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
			read() // response.interrupt sent after c2, proving c2 is already queued at epoch 0
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
			<-turn2Reached
			if tcpConn, ok := c.UnderlyingConn().(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			} else {
				_ = c.Close()
			}
			_, _ = c.UnderlyingConn().Read(make([]byte, 1))
			close(eofProcessed)
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		read()
	})
	f.base.SetModelRouterHost(&turn2BarrierModelRouterHost{
		turn2Reached: turn2Reached,
		releaseTurn2: releaseTurn2,
	})

	f.create(`,"event_id":"c1"`)
	if got := gjson.GetBytes(f.next(), "type").String(); got != "response.created" {
		t.Fatalf("first event = %s, want response.created", got)
	}
	f.create(`,"event_id":"c2"`)
	f.send(`{"type":"response.interrupt","response_id":"r1"}`)
	f.awaitCompleted()
	<-eofProcessed

	// Queue c3 after the EOF (epoch 1) while c2 is still blocked in RouteModel; the rejected
	// interrupt proves readResponsesWebsocketInput has already stamped and queued c3.
	f.create(`,"event_id":"c3"`)
	f.send(`{"type":"response.interrupt","response_id":"r1"}`)
	if interruptErr := f.nextError(); gjson.GetBytes(interruptErr, "status").Int() != http.StatusBadRequest {
		t.Fatalf("post-EOF interrupt error = %s, want 400", interruptErr)
	}
	close(releaseTurn2)

	errFrame := f.nextError()
	if gjson.GetBytes(errFrame, "error.code").String() != "upstream_websocket_not_sent" || gjson.GetBytes(errFrame, "error.event_id").String() != "c2" {
		t.Fatalf("queued create after idle EOF = %s, want upstream_websocket_not_sent for c2", errFrame)
	}

	f.awaitCompleted()
	if f.connections.Load() != 2 || f.creates.Load() != 2 {
		t.Fatalf("connections=%d creates=%d, want 2 and 2 (c1 and c3 only)", f.connections.Load(), f.creates.Load())
	}
}

// A 502 request-scoped-errors continue rule must not replay a response.create when the upstream
// websocket disconnects before response.created.
func TestResponsesWebsocketLossBeforeResponseCreatedIgnoresContinueRule(t *testing.T) {
	f := newLossFixture(t, false, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		if index == 1 {
			return // EOF before response.created
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		read()
	})

	f.create(`,"event_id":"c1"`)
	lost := f.nextError()
	if gjson.GetBytes(lost, "error.code").String() != "upstream_websocket_lost" || gjson.GetBytes(lost, "error.event_id").String() != "c1" {
		t.Fatalf("loss error = %s, want upstream_websocket_lost for c1", lost)
	}
	if f.connections.Load() != 1 || f.creates.Load() != 1 {
		t.Fatalf("after first loss: connections=%d creates=%d, want 1 and 1", f.connections.Load(), f.creates.Load())
	}

	f.create(`,"event_id":"c2"`)
	f.awaitCompleted()
	if f.connections.Load() != 2 || f.creates.Load() != 2 {
		t.Fatalf("after recovery: connections=%d creates=%d, want 2 and 2", f.connections.Load(), f.creates.Load())
	}
}

// A continuation (previous_response_id) that already passed the handler epoch check before an
// idle upstream EOF must be answered with 409 previous_response_not_found rather than 502.
func TestResponsesWebsocketQueuedContinuationBeforeIdleEOFIs409(t *testing.T) {
	turn2Reached := make(chan struct{})
	eofProcessed := make(chan struct{})
	releaseTurn2 := make(chan struct{})
	f := newLossFixture(t, false, func(index int, c *websocket.Conn, read func() []byte) {
		read()
		if index == 1 {
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCreated))
			read() // response.interrupt sent after c2, proving c2 is already queued at epoch 0
			_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
			<-turn2Reached
			if tcpConn, ok := c.UnderlyingConn().(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			} else {
				_ = c.Close()
			}
			_, _ = c.UnderlyingConn().Read(make([]byte, 1))
			close(eofProcessed)
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(lossCompleted))
		read()
	})
	f.base.SetModelRouterHost(&turn2BarrierModelRouterHost{
		turn2Reached: turn2Reached,
		releaseTurn2: releaseTurn2,
	})

	f.create(`,"event_id":"c1"`)
	if got := gjson.GetBytes(f.next(), "type").String(); got != "response.created" {
		t.Fatalf("first event = %s, want response.created", got)
	}
	f.create(`,"event_id":"c2","previous_response_id":"r1"`)
	f.send(`{"type":"response.interrupt","response_id":"r1"}`)
	f.awaitCompleted()
	<-eofProcessed
	close(releaseTurn2)

	errFrame := f.nextError()
	if gjson.GetBytes(errFrame, "status").Int() != http.StatusConflict ||
		gjson.GetBytes(errFrame, "error.code").String() != "previous_response_not_found" ||
		gjson.GetBytes(errFrame, "error.event_id").String() != "c2" {
		t.Fatalf("queued continuation after idle EOF = %s, want 409 previous_response_not_found for c2", errFrame)
	}

	f.create(`,"event_id":"c3"`)
	f.awaitCompleted()
	if f.connections.Load() != 2 || f.creates.Load() != 2 {
		t.Fatalf("connections=%d creates=%d, want 2 and 2 (c1 and c3 only)", f.connections.Load(), f.creates.Load())
	}
}
