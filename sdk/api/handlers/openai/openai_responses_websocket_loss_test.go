package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
			if ojson.GetBytesNoCopy(p, "type").String() == "response.create" {
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
		if p := f.next(); ojson.GetBytesNoCopy(p, "type").String() == "error" {
			return p
		}
	}
}

func (f *lossFixture) awaitCompleted() {
	f.t.Helper()
	for {
		p := f.next()
		switch ojson.GetBytesNoCopy(p, "type").String() {
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
			switch ojson.GetBytesNoCopy(p, "type").String() {
			case "response.completed":
				return
			case "error":
				if ojson.GetBytesNoCopy(p, "error.event_id").String() != id {
					f.t.Fatalf("unexpected error frame: %s", p)
				}
			}
			if ojson.GetBytesNoCopy(p, "type").String() == "error" {
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
	if got := ojson.GetBytesNoCopy(lost, "error.code").String(); got != "upstream_websocket_lost" {
		t.Fatalf("loss error = %s", lost)
	}
	// The turn is not replayed and a continuation cannot resume the lost socket. The full
	// create follows back to back: the refusal is not a loss and must not fence it.
	f.create(`,"previous_response_id":"r1"`)
	f.create("")
	if got := ojson.GetBytesNoCopy(f.nextError(), "error.code").String(); got != "previous_response_not_found" {
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
	if got := ojson.GetBytesNoCopy(f.nextError(), "error.code").String(); got != "previous_response_not_found" {
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
		if ojson.GetBytesNoCopy(p, "error.code").String() != w.code || ojson.GetBytesNoCopy(p, "error.event_id").String() != w.eventID {
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
			if ojson.GetBytesNoCopy(p, "error.code").String() != "upstream_websocket_lost" || ojson.GetBytesNoCopy(p, "error.event_id").String() != id {
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
	if ojson.GetBytesNoCopy(p, "error.code").String() != "upstream_websocket_lost" || ojson.GetBytesNoCopy(p, "error.event_id").String() != "c1" {
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
			if ojson.GetBytesNoCopy(p, "error.code").String() != "upstream_websocket_lost" || ojson.GetBytesNoCopy(p, "error.event_id").String() != tc.wantID {
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
	if p := f.nextError(); ojson.GetBytesNoCopy(p, "status").Int() != http.StatusForbidden {
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
	if got := ojson.GetBytesNoCopy(f.next(), "type").String(); got != "response.created" {
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
	if interruptErr := f.nextError(); ojson.GetBytesNoCopy(interruptErr, "status").Int() != http.StatusBadRequest {
		t.Fatalf("post-EOF interrupt error = %s, want 400", interruptErr)
	}
	close(releaseTurn2)

	errFrame := f.nextError()
	if ojson.GetBytesNoCopy(errFrame, "error.code").String() != "upstream_websocket_not_sent" || ojson.GetBytesNoCopy(errFrame, "error.event_id").String() != "c2" {
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
	if ojson.GetBytesNoCopy(lost, "error.code").String() != "upstream_websocket_lost" || ojson.GetBytesNoCopy(lost, "error.event_id").String() != "c1" {
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
	if got := ojson.GetBytesNoCopy(f.next(), "type").String(); got != "response.created" {
		t.Fatalf("first event = %s, want response.created", got)
	}
	f.create(`,"event_id":"c2","previous_response_id":"r1"`)
	f.send(`{"type":"response.interrupt","response_id":"r1"}`)
	f.awaitCompleted()
	<-eofProcessed
	close(releaseTurn2)

	errFrame := f.nextError()
	if ojson.GetBytesNoCopy(errFrame, "status").Int() != http.StatusConflict ||
		ojson.GetBytesNoCopy(errFrame, "error.code").String() != "previous_response_not_found" ||
		ojson.GetBytesNoCopy(errFrame, "error.event_id").String() != "c2" {
		t.Fatalf("queued continuation after idle EOF = %s, want 409 previous_response_not_found for c2", errFrame)
	}

	f.create(`,"event_id":"c3"`)
	f.awaitCompleted()
	if f.connections.Load() != 2 || f.creates.Load() != 2 {
		t.Fatalf("connections=%d creates=%d, want 2 and 2 (c1 and c3 only)", f.connections.Load(), f.creates.Load())
	}
}

func TestResponsesWebsocketEarly429RetriesAnotherCredentialWithoutClosingDownstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	early429Frames := map[string]string{
		"error_frame":     `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":600}}`,
		"response_failed": `{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","status_code":429,"message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":600}}}`,
	}

	for frameName, early429Frame := range early429Frames {
		for _, buffering := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/buffering=%t", frameName, buffering), func(t *testing.T) {
				var authAConns, authBConns atomic.Int32
				var authACreates, authBCreates atomic.Int32
				var authA429Next, authB429Next atomic.Bool
				authA429Next.Store(true)

				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
					switch authHeader {
					case "Bearer key-a":
						authAConns.Add(1)
					case "Bearer key-b":
						authBConns.Add(1)
					default:
						t.Errorf("unexpected Authorization header: %q", authHeader)
						http.Error(w, "unauthorized", http.StatusUnauthorized)
						return
					}

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
					_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))

					for {
						_, payload, errRead := c.ReadMessage()
						if errRead != nil {
							return
						}
						if ojson.GetBytesNoCopy(payload, "type").String() != "response.create" {
							continue
						}
						if authHeader == "Bearer key-a" {
							turn := authACreates.Add(1)
							if authA429Next.Load() {
								if buffering && turn == 1 {
									_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-a-buffered","status":"in_progress","output":[]}}`))
									_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.in_progress","response":{"id":"resp-a-buffered","status":"in_progress","output":[]}}`))
								}
								if errWrite := c.WriteMessage(websocket.TextMessage, []byte(early429Frame)); errWrite != nil {
									return
								}
								continue
							}
							respID := fmt.Sprintf("resp-a-%d", turn)
							_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","output":[]}}`, respID)))
							_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"id":"msg-a-%d","type":"message","role":"assistant","content":[{"type":"output_text","text":"from-a"}]}]}}`, respID, turn)))
							continue
						}
						turn := authBCreates.Add(1)
						if authB429Next.Load() {
							_ = c.WriteMessage(websocket.TextMessage, []byte(early429Frame))
							continue
						}
						respID := fmt.Sprintf("resp-b-%d", turn)
						_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","output":[]}}`, respID)))
						_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"id":"msg-b-%d","type":"message","role":"assistant","content":[{"type":"output_text","text":"from-b"}]}]}}`, respID, turn)))
					}
				}))
				t.Cleanup(upstream.Close)

				cfg := &config.Config{
					DisableCooling:      true,
					RequestRetry:        0,
					MaxRetryCredentials: 2,
					Codex: config.CodexConfig{
						StreamBootstrapBuffering: buffering,
					},
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(cfg.RequestRetry, 0, cfg.MaxRetryCredentials)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))

				modelName := "codex-early-429-" + strings.ReplaceAll(t.Name(), "/", "-")
				authAID := "auth-a-" + modelName
				authBID := "auth-b-" + modelName
				for _, spec := range []struct {
					id       string
					key      string
					priority string
				}{
					{id: authAID, key: "key-a", priority: "4"},
					{id: authBID, key: "key-b", priority: "3"},
				} {
					if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
						ID:       spec.id,
						Provider: "codex",
						Status:   coreauth.StatusActive,
						Attributes: map[string]string{
							"api_key":    spec.key,
							"base_url":   upstream.URL,
							"websockets": "true",
							"priority":   spec.priority,
						},
						Metadata: map[string]any{
							"disable_cooling": true,
						},
					}); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(spec.id, "codex", []*registry.ModelInfo{{ID: modelName}})
					id := spec.id
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}

				base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
				h := NewOpenAIResponsesAPIHandler(base)
				router := gin.New()
				router.GET("/v1/responses", h.ResponsesWebsocket)
				downstream := httptest.NewServer(router)
				t.Cleanup(downstream.Close)
				wsURL := "ws" + strings.TrimPrefix(downstream.URL, "http") + "/v1/responses"

				// 1. Request 1 selects auth-a first, receives early 429, fails over to auth-b
				// within the same downstream WebSocket request, and completes without closing downstream.
				conn1, resp1, errDial1 := websocket.DefaultDialer.Dial(wsURL, nil)
				if resp1 != nil && resp1.Body != nil {
					defer func() {
						if errClose := resp1.Body.Close(); errClose != nil {
							t.Logf("close handshake response body: %v", errClose)
						}
					}()
				}
				if errDial1 != nil {
					t.Fatalf("dial first downstream websocket: %v", errDial1)
				}
				defer func() {
					if errClose := conn1.Close(); errClose != nil {
						t.Logf("close first downstream websocket: %v", errClose)
					}
				}()

				req1 := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1","role":"user","content":"hello"}]}`, modelName)
				if errWrite := conn1.WriteMessage(websocket.TextMessage, []byte(req1)); errWrite != nil {
					t.Fatalf("write request 1: %v", errWrite)
				}

				var completed1 []byte
				for {
					_ = conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
					_, frame, errRead := conn1.ReadMessage()
					if errRead != nil {
						t.Fatalf("request 1 downstream websocket closed before completion: %v (authACreates=%d authBCreates=%d)", errRead, authACreates.Load(), authBCreates.Load())
					}
					if gotID := ojson.GetBytesNoCopy(frame, "response.id").String(); gotID == "resp-a-buffered" {
						t.Fatalf("buffered handshake from failed credential A leaked to downstream: %s", frame)
					}
					if ojson.GetBytesNoCopy(frame, "type").String() == "response.completed" {
						completed1 = frame
						break
					}
				}
				if got := ojson.GetBytesNoCopy(completed1, "response.id").String(); got != "resp-b-1" {
					t.Fatalf("request 1 completed response.id = %q, want resp-b-1; frame=%s", got, completed1)
				}
				if authACreates.Load() != 1 || authBCreates.Load() != 1 {
					t.Fatalf("after request 1: authACreates=%d authBCreates=%d, want 1 and 1", authACreates.Load(), authBCreates.Load())
				}

				followup1 := fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"resp-b-1","input":[{"type":"message","id":"msg-1b","role":"user","content":"followup on b"}]}`, modelName)
				if errWrite := conn1.WriteMessage(websocket.TextMessage, []byte(followup1)); errWrite != nil {
					t.Fatalf("write followup on conn1: %v", errWrite)
				}
				var completed1b []byte
				for {
					_ = conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
					_, frame, errRead := conn1.ReadMessage()
					if errRead != nil {
						t.Fatalf("followup on conn1 closed before completion: %v", errRead)
					}
					if ojson.GetBytesNoCopy(frame, "type").String() == "response.completed" {
						completed1b = frame
						break
					}
				}
				if got := ojson.GetBytesNoCopy(completed1b, "response.id").String(); got != "resp-b-2" {
					t.Fatalf("followup on conn1 response.id = %q, want resp-b-2; frame=%s", got, completed1b)
				}
				if authBConns.Load() != 1 || authBCreates.Load() != 2 {
					t.Fatalf("after followup on conn1: authBConns=%d authBCreates=%d, want 1 and 2", authBConns.Load(), authBCreates.Load())
				}

				// 2. A subsequent independent request can still select auth-a when cooling is disabled.
				authA429Next.Store(false)
				conn2, resp2, errDial2 := websocket.DefaultDialer.Dial(wsURL, nil)
				if resp2 != nil && resp2.Body != nil {
					defer func() {
						if errClose := resp2.Body.Close(); errClose != nil {
							t.Logf("close second handshake response body: %v", errClose)
						}
					}()
				}
				if errDial2 != nil {
					t.Fatalf("dial second downstream websocket: %v", errDial2)
				}
				defer func() {
					if errClose := conn2.Close(); errClose != nil {
						t.Logf("close second downstream websocket: %v", errClose)
					}
				}()

				req2 := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-2","role":"user","content":"independent"}]}`, modelName)
				if errWrite := conn2.WriteMessage(websocket.TextMessage, []byte(req2)); errWrite != nil {
					t.Fatalf("write request 2: %v", errWrite)
				}
				var completed2 []byte
				for {
					_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
					_, frame, errRead := conn2.ReadMessage()
					if errRead != nil {
						t.Fatalf("request 2 downstream websocket closed before completion: %v", errRead)
					}
					if ojson.GetBytesNoCopy(frame, "type").String() == "response.completed" {
						completed2 = frame
						break
					}
				}
				if got := ojson.GetBytesNoCopy(completed2, "response.id").String(); got != "resp-a-2" {
					t.Fatalf("request 2 completed response.id = %q, want resp-a-2; frame=%s", got, completed2)
				}
				if authACreates.Load() != 2 || authBCreates.Load() != 2 {
					t.Fatalf("after request 2: authACreates=%d authBCreates=%d, want 2 and 2", authACreates.Load(), authBCreates.Load())
				}

				// 3. A continuation with previous_response_id on the pinned connection refuses cross-credential replay.
				authA429Next.Store(true)
				req3 := fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"resp-a-2","input":[{"type":"message","id":"msg-3","role":"user","content":"continuation"}]}`, modelName)
				if errWrite := conn2.WriteMessage(websocket.TextMessage, []byte(req3)); errWrite != nil {
					t.Fatalf("write request 3: %v", errWrite)
				}
				_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, _, errReadClose := conn2.ReadMessage()
				var closeErr *websocket.CloseError
				if !errors.As(errReadClose, &closeErr) || closeErr.Code != websocket.CloseServiceRestart || closeErr.Text != wsHTTPReplayRequiredCloseReason {
					t.Fatalf("continuation failure close = %v, want %d %q", errReadClose, websocket.CloseServiceRestart, wsHTTPReplayRequiredCloseReason)
				}
				if authACreates.Load() != 3 || authBCreates.Load() != 2 {
					t.Fatalf("after continuation failure: authACreates=%d authBCreates=%d, want 3 and 2 (must not replay previous_response_id on auth-b)", authACreates.Load(), authBCreates.Load())
				}

				// 4. When all eligible credentials return early 429, the downstream WebSocket receives a terminal 429 error frame before closing.
				authA429Next.Store(true)
				authB429Next.Store(true)
				conn3, resp3, errDial3 := websocket.DefaultDialer.Dial(wsURL, nil)
				if resp3 != nil && resp3.Body != nil {
					defer func() {
						if errClose := resp3.Body.Close(); errClose != nil {
							t.Logf("close third handshake response body: %v", errClose)
						}
					}()
				}
				if errDial3 != nil {
					t.Fatalf("dial third downstream websocket: %v", errDial3)
				}
				defer func() {
					if errClose := conn3.Close(); errClose != nil {
						t.Logf("close third downstream websocket: %v", errClose)
					}
				}()
				req4 := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-4","role":"user","content":"exhausted"}]}`, modelName)
				if errWrite := conn3.WriteMessage(websocket.TextMessage, []byte(req4)); errWrite != nil {
					t.Fatalf("write request 4: %v", errWrite)
				}
				_ = conn3.SetReadDeadline(time.Now().Add(5 * time.Second))
				msgType4, frame4, errRead4 := conn3.ReadMessage()
				if errRead4 != nil {
					t.Fatalf("expected terminal 429 error frame after exhausting credentials, got read error: %v", errRead4)
				}
				if msgType4 != websocket.TextMessage {
					t.Fatalf("frame4 msgType = %d, want TextMessage (%d)", msgType4, websocket.TextMessage)
				}
				if got := ojson.GetBytesNoCopy(frame4, "type").String(); got != "error" {
					t.Fatalf("frame4 type = %q, want error: %s", got, frame4)
				}
				if got := int(ojson.GetBytesNoCopy(frame4, "status").Int()); got != http.StatusTooManyRequests {
					t.Fatalf("frame4 status = %d, want %d: %s", got, http.StatusTooManyRequests, frame4)
				}
				if got := ojson.GetBytesNoCopy(frame4, "error.type").String(); got != "usage_limit_reached" {
					t.Fatalf("frame4 error.type = %q, want usage_limit_reached: %s", got, frame4)
				}
				if got := int(ojson.GetBytesNoCopy(frame4, "error.resets_in_seconds").Int()); got != 600 {
					t.Fatalf("frame4 error.resets_in_seconds = %d, want 600: %s", got, frame4)
				}
				if got := ojson.GetBytesNoCopy(frame4, "headers.Retry-After").String(); got == "" {
					t.Fatalf("frame4 headers.Retry-After is empty: %s", frame4)
				}
				if _, extraFrame, errAfter := conn3.ReadMessage(); errAfter == nil {
					t.Fatalf("expected socket close after terminal 429 error frame, got frame: %s", extraFrame)
				}
				if authACreates.Load() != 4 || authBCreates.Load() != 3 {
					t.Fatalf("after all credentials exhausted: authACreates=%d authBCreates=%d, want 4 and 3", authACreates.Load(), authBCreates.Load())
				}
			})
		}
	}
}

func TestResponsesWebsocketTerminal429WhenAllCredentialsExhaustedOrCooling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const early429Frame = `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":600}}`

	var creates atomic.Int32
	var return429 atomic.Bool

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))

		for {
			_, payload, errRead := c.ReadMessage()
			if errRead != nil {
				return
			}
			if ojson.GetBytesNoCopy(payload, "type").String() != "response.create" {
				continue
			}
			turn := creates.Add(1)
			if return429.Load() {
				_ = c.WriteMessage(websocket.TextMessage, []byte(early429Frame))
				continue
			}
			respID := fmt.Sprintf("resp-single-%d", turn)
			_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","output":[]}}`, respID)))
			_ = c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"id":"msg-%d","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`, respID, turn)))
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := &config.Config{
		DisableCooling:      false,
		RequestRetry:        0,
		MaxRetryCredentials: 1,
	}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(cfg.RequestRetry, 0, cfg.MaxRetryCredentials)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))

	modelName := "codex-single-cred-429"
	authID := "auth-single-429"
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":    "key-single",
			"base_url":   upstream.URL,
			"websockets": "true",
		},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	t.Cleanup(downstream.Close)
	wsURL := "ws" + strings.TrimPrefix(downstream.URL, "http") + "/v1/responses"

	// 1. First turn succeeds and pins the single credential.
	conn1, resp1, errDial1 := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp1 != nil && resp1.Body != nil {
		defer func() {
			if errClose := resp1.Body.Close(); errClose != nil {
				t.Logf("close first handshake response body: %v", errClose)
			}
		}()
	}
	if errDial1 != nil {
		t.Fatalf("dial first downstream websocket: %v", errDial1)
	}
	defer func() {
		if errClose := conn1.Close(); errClose != nil {
			t.Logf("close first downstream websocket: %v", errClose)
		}
	}()

	req1 := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1","role":"user","content":"hello"}]}`, modelName)
	if errWrite := conn1.WriteMessage(websocket.TextMessage, []byte(req1)); errWrite != nil {
		t.Fatalf("write request 1: %v", errWrite)
	}
	for {
		_ = conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, frame, errRead := conn1.ReadMessage()
		if errRead != nil {
			t.Fatalf("request 1 failed: %v", errRead)
		}
		if ojson.GetBytesNoCopy(frame, "type").String() == "response.completed" {
			break
		}
	}

	// 2. Continuation on the pinned single credential hits 429 usage_limit_reached.
	// With no alternative credential to fail over to, it must emit a terminal 429 error frame directly instead of 1012.
	return429.Store(true)
	req2 := fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"resp-single-1","input":[{"type":"message","id":"msg-2","role":"user","content":"followup"}]}`, modelName)
	if errWrite := conn1.WriteMessage(websocket.TextMessage, []byte(req2)); errWrite != nil {
		t.Fatalf("write request 2: %v", errWrite)
	}
	_ = conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType2, frame2, errRead2 := conn1.ReadMessage()
	if errRead2 != nil {
		t.Fatalf("expected terminal 429 error frame on single-credential continuation, got error: %v", errRead2)
	}
	if msgType2 != websocket.TextMessage {
		t.Fatalf("frame2 msgType = %d, want TextMessage (%d)", msgType2, websocket.TextMessage)
	}
	if got := ojson.GetBytesNoCopy(frame2, "type").String(); got != "error" {
		t.Fatalf("frame2 type = %q, want error: %s", got, frame2)
	}
	if got := int(ojson.GetBytesNoCopy(frame2, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("frame2 status = %d, want %d: %s", got, http.StatusTooManyRequests, frame2)
	}
	if got := ojson.GetBytesNoCopy(frame2, "error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("frame2 error.type = %q, want usage_limit_reached: %s", got, frame2)
	}
	if got := int(ojson.GetBytesNoCopy(frame2, "error.resets_in_seconds").Int()); got != 600 {
		t.Fatalf("frame2 error.resets_in_seconds = %d, want 600: %s", got, frame2)
	}
	if _, extraFrame, errAfter := conn1.ReadMessage(); errAfter == nil {
		t.Fatalf("expected socket close after terminal 429 error frame, got frame: %s", extraFrame)
	}

	// 3. The single credential is now cooling down. A subsequent connection must receive
	// a terminal 429 model_cooldown error frame with reset_seconds and Retry-After.
	conn2, resp2, errDial2 := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp2 != nil && resp2.Body != nil {
		defer func() {
			if errClose := resp2.Body.Close(); errClose != nil {
				t.Logf("close second handshake response body: %v", errClose)
			}
		}()
	}
	if errDial2 != nil {
		t.Fatalf("dial second downstream websocket: %v", errDial2)
	}
	defer func() {
		if errClose := conn2.Close(); errClose != nil {
			t.Logf("close second downstream websocket: %v", errClose)
		}
	}()

	req3 := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-3","role":"user","content":"while cooling"}]}`, modelName)
	if errWrite := conn2.WriteMessage(websocket.TextMessage, []byte(req3)); errWrite != nil {
		t.Fatalf("write request 3: %v", errWrite)
	}
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType3, frame3, errRead3 := conn2.ReadMessage()
	if errRead3 != nil {
		t.Fatalf("expected terminal 429 model_cooldown error frame, got error: %v", errRead3)
	}
	if msgType3 != websocket.TextMessage {
		t.Fatalf("frame3 msgType = %d, want TextMessage (%d)", msgType3, websocket.TextMessage)
	}
	if got := ojson.GetBytesNoCopy(frame3, "type").String(); got != "error" {
		t.Fatalf("frame3 type = %q, want error: %s", got, frame3)
	}
	if got := int(ojson.GetBytesNoCopy(frame3, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("frame3 status = %d, want %d: %s", got, http.StatusTooManyRequests, frame3)
	}
	if got := ojson.GetBytesNoCopy(frame3, "error.code").String(); got != "model_cooldown" {
		t.Fatalf("frame3 error.code = %q, want model_cooldown: %s", got, frame3)
	}
	if got := int(ojson.GetBytesNoCopy(frame3, "error.reset_seconds").Int()); got <= 0 {
		t.Fatalf("frame3 error.reset_seconds = %d, want > 0: %s", got, frame3)
	}
	if got := ojson.GetBytesNoCopy(frame3, "headers.Retry-After").String(); got == "" {
		t.Fatalf("frame3 headers.Retry-After is empty: %s", frame3)
	}
	if _, extraFrame, errAfter := conn2.ReadMessage(); errAfter == nil {
		t.Fatalf("expected socket close after terminal 429 model_cooldown frame, got frame: %s", extraFrame)
	}
	if creates.Load() != 2 {
		t.Fatalf("upstream creates = %d, want 2 (cooling credential must not be called again)", creates.Load())
	}
}

func TestResponsesWebsocketPinned429ExposesErrorWhenOnlyAlternativeHasExpiredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelName = "codex-expired-alt-429"
	upstreamUpgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upstreamUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		defer func() {
			_ = conn.Close()
		}()
		for turn := 1; ; turn++ {
			_, raw, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			if ojson.GetBytesNoCopy(raw, "type").String() != "response.create" {
				return
			}
			if turn == 1 {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`))
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":600}}`))
			return
		}
	}))
	defer upstream.Close()

	cfg := &config.Config{
		SDKConfig:           config.SDKConfig{PassthroughHeaders: true},
		RequestRetry:        0,
		MaxRetryCredentials: 0,
		QuotaExceeded: config.QuotaExceeded{
			SwitchProject:      true,
			SwitchPreviewModel: true,
		},
	}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(cfg.RequestRetry, 0, cfg.MaxRetryCredentials)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))

	authA := &coreauth.Auth{
		ID:       "auth-active-429",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":    "key-active",
			"base_url":   upstream.URL,
			"websockets": "true",
		},
	}
	authB := &coreauth.Auth{
		ID:       "auth-expired-alt",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":    "key-expired",
			"base_url":   upstream.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{
			"access_token": "expired-token",
			"expired":      time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	for _, a := range []*coreauth.Auth{authA, authB} {
		if _, errReg := manager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("register auth %s: %v", a.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: modelName}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	baseHandler := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	handler := NewOpenAIResponsesAPIHandler(baseHandler)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	wsURL := "ws" + strings.TrimPrefix(downstream.URL, "http") + "/v1/responses"
	conn, resp, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() {
			_ = resp.Body.Close()
		}()
	}
	if errDial != nil {
		t.Fatalf("dial downstream websocket: %v", errDial)
	}
	defer func() {
		_ = conn.Close()
	}()

	req1 := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1","role":"user","content":"turn 1"}]}`, modelName)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(req1)); errWrite != nil {
		t.Fatalf("write request 1: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, frame1, errRead1 := conn.ReadMessage(); errRead1 != nil || ojson.GetBytesNoCopy(frame1, "type").String() != "response.completed" {
		t.Fatalf("read request 1: frame=%s err=%v", frame1, errRead1)
	}

	req2 := fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"resp-1","input":[{"type":"message","id":"msg-2","role":"user","content":"turn 2"}]}`, modelName)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(req2)); errWrite != nil {
		t.Fatalf("write request 2: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, frame2, errRead2 := conn.ReadMessage()
	if errRead2 != nil {
		t.Fatalf("expected terminal 429 error frame when only alternative credential is expired, got error: %v", errRead2)
	}
	if got := ojson.GetBytesNoCopy(frame2, "type").String(); got != "error" {
		t.Fatalf("frame2 type = %q, want error: %s", got, frame2)
	}
	if got := int(ojson.GetBytesNoCopy(frame2, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("frame2 status = %d, want 429: %s", got, frame2)
	}
	if got := ojson.GetBytesNoCopy(frame2, "error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("frame2 error.type = %q, want usage_limit_reached: %s", got, frame2)
	}
}

type homeCallbackStreamExecutor struct {
	provider string
	streamFn func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (e *homeCallbackStreamExecutor) Identifier() string {
	if e != nil && strings.TrimSpace(e.provider) != "" {
		return strings.TrimSpace(e.provider)
	}
	return "codex"
}

func (*homeCallbackStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *homeCallbackStreamExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return e.streamFn(ctx, auth, req, opts)
}

func (*homeCallbackStreamExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New("not implemented")
}

func (*homeCallbackStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*homeCallbackStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestResponsesWebsocketHomePinned429ExposesErrorWhenNoAlternativeAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dispatcher := &homeResponsesWebsocketDispatcher{}
	var turnCount atomic.Int32
	executor := &homeCallbackStreamExecutor{
		provider: "codex",
		streamFn: func(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			if turnCount.Add(1) == 1 {
				if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
					lifecycle.Retain()
				}
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-home-1","output":[]}}`)}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			return nil, websocketPinnedFailoverStatusError{
				status: http.StatusTooManyRequests,
				msg:    `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"team","resets_in_seconds":600}}`,
			}
		},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient("home-responses-websocket-auth", "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("home-responses-websocket-auth")
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, resp, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		defer func() {
			_ = resp.Body.Close()
		}()
	}
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)); errWrite != nil {
		t.Fatalf("write turn 1: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, frame1, errRead1 := conn.ReadMessage(); errRead1 != nil || ojson.GetBytesNoCopy(frame1, "type").String() != "response.completed" {
		t.Fatalf("read turn 1: frame=%s err=%v", frame1, errRead1)
	}

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp-home-1","input":[]}`)); errWrite != nil {
		t.Fatalf("write turn 2: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, frame2, errRead2 := conn.ReadMessage()
	if errRead2 != nil {
		t.Fatalf("expected terminal 429 error frame on single-credential Home continuation, got error: %v", errRead2)
	}
	if got := ojson.GetBytesNoCopy(frame2, "type").String(); got != "error" {
		t.Fatalf("frame2 type = %q, want error: %s", got, frame2)
	}
	if got := int(ojson.GetBytesNoCopy(frame2, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("frame2 status = %d, want 429: %s", got, frame2)
	}
	if got := ojson.GetBytesNoCopy(frame2, "error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("frame2 error.type = %q, want usage_limit_reached: %s", got, frame2)
	}
}

type authHeaderCheckingHomeDispatcher struct {
	mu           sync.Mutex
	requiredAuth string
	authIDs      []string
}

func (*authHeaderCheckingHomeDispatcher) HeartbeatOK() bool       { return true }
func (*authHeaderCheckingHomeDispatcher) AbortAmbiguousDispatch() {}

func (d *authHeaderCheckingHomeDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *authHeaderCheckingHomeDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, headers http.Header, _ int, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error) {
	if strings.TrimSpace(headers.Get("Authorization")) != d.requiredAuth {
		return []byte(`{"error":{"code":"auth_not_found","message":"missing or invalid client authorization header"}}`), nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	excluded := make(map[string]struct{}, len(excludedAuthIDs))
	for _, id := range excludedAuthIDs {
		excluded[id] = struct{}{}
	}
	for _, id := range d.authIDs {
		if pinnedAuthID != "" && id != pinnedAuthID {
			continue
		}
		if _, skip := excluded[id]; skip {
			continue
		}
		return json.Marshal(coreauth.Auth{
			ID:       id,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"websockets": "true",
			},
		})
	}
	return []byte(`{"error":{"code":"auth_not_found","message":"no more auths"}}`), nil
}

func TestResponsesWebsocketHomePinned429ReplaysWith1012WhenAuthenticatedAlternativeExists(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dispatcher := &authHeaderCheckingHomeDispatcher{
		requiredAuth: "Bearer client-secret-key",
		authIDs:      []string{"home-auth-a", "home-auth-b"},
	}
	var turnCount atomic.Int32
	executor := &homeCallbackStreamExecutor{
		provider: "codex",
		streamFn: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			if turnCount.Add(1) == 1 {
				if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
					lifecycle.Retain()
				}
				chunks := make(chan coreexecutor.StreamChunk, 1)
				chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-home-1","output":[]}}`)}
				close(chunks)
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			}
			if auth != nil && auth.ID == "home-auth-a" {
				return nil, websocketPinnedFailoverStatusError{
					status: http.StatusTooManyRequests,
					msg:    `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"team","resets_in_seconds":600}}`,
				}
			}
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-home-2","output":[]}}`)}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)
	for _, id := range []string{"home-auth-a", "home-auth-b"} {
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("home-auth-a")
		registry.GetGlobalRegistry().UnregisterClient("home-auth-b")
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	dialHeaders := http.Header{"Authorization": {"Bearer client-secret-key"}}
	conn, resp, errDial := websocket.DefaultDialer.Dial(wsURL, dialHeaders)
	if resp != nil && resp.Body != nil {
		defer func() {
			_ = resp.Body.Close()
		}()
	}
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() {
		_ = conn.Close()
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)); errWrite != nil {
		t.Fatalf("write turn 1: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, frame1, errRead1 := conn.ReadMessage(); errRead1 != nil || ojson.GetBytesNoCopy(frame1, "type").String() != "response.completed" {
		t.Fatalf("read turn 1: frame=%s err=%v", frame1, errRead1)
	}

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp-home-1","input":[]}`)); errWrite != nil {
		t.Fatalf("write turn 2: %v", errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, frame2, errRead2 := conn.ReadMessage()
	if errRead2 == nil {
		t.Fatalf("expected 1012 replay close when authenticated Home alternative exists, got frame: %s", frame2)
	}
	if !websocket.IsCloseError(errRead2, websocket.CloseServiceRestart) {
		t.Fatalf("turn 2 close error = %v, want 1012 ServiceRestart", errRead2)
	}
}

type websocketHintTestError struct {
	status int
	msg    string
	hint   *time.Duration
}

func (e websocketHintTestError) Error() string              { return e.msg }
func (e websocketHintTestError) StatusCode() int            { return e.status }
func (e websocketHintTestError) RetryAfter() *time.Duration { return e.hint }

type websocketTwoAuthHomeDispatcher struct {
	mu      sync.Mutex
	authIDs []string
}

func (*websocketTwoAuthHomeDispatcher) HeartbeatOK() bool       { return true }
func (*websocketTwoAuthHomeDispatcher) AbortAmbiguousDispatch() {}

func (d *websocketTwoAuthHomeDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithConstraints(ctx, model, sessionID, headers, count, nil, "")
}

func (d *websocketTwoAuthHomeDispatcher) RPopAuthWithConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, excludedAuthIDs []string, _ string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	excluded := make(map[string]struct{}, len(excludedAuthIDs))
	for _, id := range excludedAuthIDs {
		excluded[id] = struct{}{}
	}
	for _, id := range d.authIDs {
		if _, skip := excluded[id]; skip {
			continue
		}
		return json.Marshal(coreauth.Auth{
			ID:       id,
			Provider: "codex",
			Status:   coreauth.StatusActive,
		})
	}
	return []byte(`{"error":{"code":"request_retry_exceeded","message":"no more auths"}}`), nil
}

func TestBuildResponsesWebsocketErrorPayloadSuppressesStaleRetryAfterWhenTrustedDeadlineHasNoHeader(t *testing.T) {
	zero := time.Duration(0)
	positive := 256 * time.Second
	dispatcher := &websocketTwoAuthHomeDispatcher{authIDs: []string{"home-a", "home-b"}}
	executor := &homeCallbackStreamExecutor{
		provider: "codex",
		streamFn: func(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			hint := &positive
			if auth != nil && auth.ID == "home-a" {
				hint = &zero
			}
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Err: websocketHintTestError{
				status: http.StatusTooManyRequests,
				msg:    `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
				hint:   hint,
			}}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"Retry-After": {"30"}, "X-Request-Id": {"req-1"}},
				Chunks:  chunks,
			}, nil
		},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, time.Second, 2)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	result, errExec := manager.ExecuteStream(context.Background(), []string{"codex"}, coreexecutor.Request{Model: "gpt-5.4"}, coreexecutor.Options{Stream: true})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}
	chunk, ok := <-result.Chunks
	if !ok || chunk.Err == nil {
		t.Fatalf("stream chunk = %#v, %t; want error", chunk, ok)
	}
	if !coreauth.HasTrustedRetryDeadline(chunk.Err) {
		t.Fatal("expected HasTrustedRetryDeadline(chunk.Err) = true")
	}
	if got := coreauth.SafeResponseHeaders(chunk.Err).Get("Retry-After"); got != "" {
		t.Fatalf("expected empty SafeResponseHeaders Retry-After, got %q", got)
	}

	payload, errBuild := buildResponsesWebsocketErrorPayload(&interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      chunk.Err,
		Addon:      http.Header{"Retry-After": {"30"}, "X-Request-Id": {"req-1"}},
	})
	if errBuild != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload() error = %v", errBuild)
	}
	if got := ojson.GetBytesNoCopy(payload, "headers.Retry-After").String(); got != "" {
		t.Fatalf("stale Addon Retry-After leaked into websocket error payload: %q (%s)", got, payload)
	}
	if got := ojson.GetBytesNoCopy(payload, "headers.X-Request-Id").String(); got != "req-1" {
		t.Fatalf("headers.X-Request-Id = %q, want req-1 (%s)", got, payload)
	}
}
