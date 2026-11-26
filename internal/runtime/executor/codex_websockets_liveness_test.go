package executor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type deadlineObservingConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *deadlineObservingConn) SetReadDeadline(t time.Time) error {
	errSet := c.Conn.SetReadDeadline(t)
	if errSet == nil {
		select {
		case c.deadlines <- t:
		default:
		}
	}
	return errSet
}

func dialDeadlineObservingWebsocket(t *testing.T, wsURL string) (*websocket.Conn, *deadlineObservingConn) {
	t.Helper()
	var observed *deadlineObservingConn
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, errDial := (&net.Dialer{}).DialContext(ctx, network, addr)
			if errDial != nil {
				return nil, errDial
			}
			observed = &deadlineObservingConn{
				Conn:      raw,
				deadlines: make(chan time.Time, 64),
			}
			return observed, nil
		},
	}
	conn, _, errDial := dialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	if observed == nil {
		t.Fatal("expected observed underlying net.Conn")
	}
	return conn, observed
}

func drainDeadlines(ch <-chan time.Time) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func waitForRenewedDeadline(t *testing.T, ch <-chan time.Time, after time.Time) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.After(after) {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for read deadline after %v", after)
		}
	}
}

// setTestUnderlyingReadDeadline uses net.Conn.SetReadDeadline directly so test
// goroutines never invoke gorilla/websocket.Conn reader methods concurrently
// with an active ReadMessage call.
func setTestUnderlyingReadDeadline(t *testing.T, conn *websocket.Conn, deadline time.Time) {
	t.Helper()
	underlying := conn.UnderlyingConn()
	if underlying == nil {
		t.Fatal("expected non-nil underlying net.Conn")
	}
	if errDeadline := underlying.SetReadDeadline(deadline); errDeadline != nil {
		t.Fatalf("set underlying read deadline: %v", errDeadline)
	}
}

func assertTimeoutErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected read timeout error, got nil")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout net.Error, got %T: %v", err, err)
	}
}

func waitForPingPong(t *testing.T, serverConn *websocket.Conn, pongCh <-chan string, payload string) {
	t.Helper()
	if errPing := serverConn.WriteControl(websocket.PingMessage, []byte(payload), time.Now().Add(2*time.Second)); errPing != nil {
		t.Fatalf("write ping %q: %v", payload, errPing)
	}
	select {
	case got := <-pongCh:
		if got != payload {
			t.Fatalf("pong payload = %q, want %q", got, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for pong %q", payload)
	}
}

func waitPastDeadline(deadline time.Time) {
	if wait := time.Until(deadline) + 80*time.Millisecond; wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		<-timer.C
	}
}

func TestCodexWebsocketActiveResponseRenewsDeadlineOnPing(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	pongCh := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		conn.SetPongHandler(func(appData string) error {
			pongCh <- appData
			return nil
		})
		serverConnCh <- conn
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	client, observed := dialDeadlineObservingWebsocket(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))
	serverConn := <-serverConnCh
	defer func() {
		_ = serverConn.Close()
	}()

	exec := NewCodexWebsocketsExecutor(nil)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "liveness-active-ping"
	defer exec.CloseExecutionSession(sessionID)

	sess := exec.getOrCreateSession(sessionID)
	sess.conn = client
	sess.connCloser = newWebsocketConnectionCloser(client)
	sess.configureConn(client)
	readCh := sess.activate(client)
	go exec.readUpstreamLoop(sess, client)

	if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`)); errWrite != nil {
		t.Fatalf("write response.created: %v", errWrite)
	}
	select {
	case ev := <-readCh:
		if ev.err != nil {
			t.Fatalf("unexpected error reading response.created: %v", ev.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for response.created")
	}

	// Ping #0 runs deadline renewal before writing Pong, so receiving Pong proves
	// readUpstreamLoop is blocked inside conn.ReadMessage() with no pending renewal.
	waitForPingPong(t, serverConn, pongCh, "sync-0")
	drainDeadlines(observed.deadlines)

	// Tighten the active read deadline to simulate approaching the 300s boundary.
	shortDeadline := time.Now().Add(120 * time.Millisecond)
	setTestUnderlyingReadDeadline(t, client, shortDeadline)
	drainDeadlines(observed.deadlines)

	// Upstream heartbeat while the response is still active must renew the deadline.
	waitForPingPong(t, serverConn, pongCh, "keepalive-1")
	waitForRenewedDeadline(t, observed.deadlines, shortDeadline.Add(time.Minute))
	waitPastDeadline(shortDeadline)

	if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`)); errWrite != nil {
		t.Fatalf("write response.completed: %v", errWrite)
	}
	select {
	case ev := <-readCh:
		if ev.err != nil {
			t.Fatalf("active response failed after valid ping/pong: %v", ev.err)
		}
		if gotType := ojson.GetBytesNoCopy(ev.payload, "type").String(); gotType != "response.completed" {
			t.Fatalf("event type = %q, want response.completed", gotType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for response.completed")
	}
	sess.clearActive(client, readCh)
}

func TestCodexWebsocketRawConnRenewsDeadlineOnPing(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	pongCh := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		conn.SetPongHandler(func(appData string) error {
			pongCh <- appData
			return nil
		})
		serverConnCh <- conn
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	wsURL := "ws" + strings.TrimPrefix(upstream.URL, "http")
	client, observed := dialDeadlineObservingWebsocket(t, wsURL)
	defer func() {
		_ = client.Close()
	}()
	serverConn := <-serverConnCh
	defer func() {
		_ = serverConn.Close()
	}()

	configureRawCodexWebsocketConn(client, "raw-auth", wsURL)

	type readResult struct {
		payload []byte
		err     error
	}
	resCh := make(chan readResult, 1)
	go func() {
		_, payload, errRead := readCodexWebsocketMessage(context.Background(), nil, client, nil)
		resCh <- readResult{payload: payload, err: errRead}
	}()

	waitForPingPong(t, serverConn, pongCh, "raw-sync-0")
	drainDeadlines(observed.deadlines)

	shortDeadline := time.Now().Add(120 * time.Millisecond)
	setTestUnderlyingReadDeadline(t, client, shortDeadline)
	drainDeadlines(observed.deadlines)

	waitForPingPong(t, serverConn, pongCh, "raw-keepalive-1")
	waitForRenewedDeadline(t, observed.deadlines, shortDeadline.Add(time.Minute))
	waitPastDeadline(shortDeadline)

	if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r-raw","output":[]}}`)); errWrite != nil {
		t.Fatalf("write response.completed: %v", errWrite)
	}
	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("raw connection read failed after valid ping/pong: %v", res.err)
		}
		if gotType := ojson.GetBytesNoCopy(res.payload, "type").String(); gotType != "response.completed" {
			t.Fatalf("event type = %q, want response.completed", gotType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for raw connection response.completed")
	}
}

func TestCodexWebsocketCompletedIdleSessionExpiresDespitePing(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	pongCh := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		conn.SetPongHandler(func(appData string) error {
			pongCh <- appData
			return nil
		})
		serverConnCh <- conn
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	client, observed := dialDeadlineObservingWebsocket(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))
	serverConn := <-serverConnCh
	defer func() {
		_ = serverConn.Close()
	}()

	exec := NewCodexWebsocketsExecutor(nil)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "liveness-idle-expire"
	defer exec.CloseExecutionSession(sessionID)

	sess := exec.getOrCreateSession(sessionID)
	sess.conn = client
	sess.connCloser = newWebsocketConnectionCloser(client)
	sess.configureConn(client)
	readCh := sess.activate(client)
	go exec.readUpstreamLoop(sess, client)

	for _, msg := range []string{
		`{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
		`{"type":"response.completed","response":{"id":"r1","output":[]}}`,
	} {
		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
			t.Fatalf("write %s: %v", msg, errWrite)
		}
		select {
		case ev := <-readCh:
			if ev.err != nil {
				t.Fatalf("unexpected error: %v", ev.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out reading turn 1 event")
		}
	}
	sess.clearActive(client, readCh)

	// Confirm readUpstreamLoop is blocked in ReadMessage() during the idle window.
	waitForPingPong(t, serverConn, pongCh, "idle-sync-0")
	drainDeadlines(observed.deadlines)

	shortDeadline := time.Now().Add(120 * time.Millisecond)
	setTestUnderlyingReadDeadline(t, client, shortDeadline)
	drainDeadlines(observed.deadlines)

	// Ping on a completed idle session still gets a Pong, but must NOT extend the idle deadline.
	waitForPingPong(t, serverConn, pongCh, "idle-ping-1")
	select {
	case unexpected := <-observed.deadlines:
		t.Fatalf("idle ping unexpectedly updated read deadline to %v", unexpected)
	default:
	}

	select {
	case errDisc := <-sess.upstreamDisconnectCh:
		assertTimeoutErr(t, errDisc)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("completed idle session was kept alive past its deadline by upstream ping")
	}
}

func TestCodexWebsocketDeadPeerStillTimesOutDuringActiveResponse(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	pongCh := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		conn.SetPongHandler(func(appData string) error {
			pongCh <- appData
			return nil
		})
		serverConnCh <- conn
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	client, observed := dialDeadlineObservingWebsocket(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))
	serverConn := <-serverConnCh
	defer func() {
		_ = serverConn.Close()
	}()

	exec := NewCodexWebsocketsExecutor(nil)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "liveness-dead-peer"
	defer exec.CloseExecutionSession(sessionID)

	sess := exec.getOrCreateSession(sessionID)
	sess.conn = client
	sess.connCloser = newWebsocketConnectionCloser(client)
	sess.configureConn(client)
	readCh := sess.activate(client)
	go exec.readUpstreamLoop(sess, client)

	if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`)); errWrite != nil {
		t.Fatalf("write response.created: %v", errWrite)
	}
	select {
	case ev := <-readCh:
		if ev.err != nil {
			t.Fatalf("unexpected error: %v", ev.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for response.created")
	}

	// Ping #0 extends the deadline before writing Pong, so by the time Pong arrives
	// no further SetReadDeadline call can race with setting shortDeadline below.
	waitForPingPong(t, serverConn, pongCh, "dead-sync-0")
	drainDeadlines(observed.deadlines)

	shortDeadline := time.Now().Add(120 * time.Millisecond)
	setTestUnderlyingReadDeadline(t, client, shortDeadline)

	// No heartbeat and no data: active turn must still time out.
	select {
	case ev := <-readCh:
		assertTimeoutErr(t, ev.err)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("unresponsive peer did not time out")
	}
}

func TestCodexWebsocketReusedSessionRefreshesDeadlineForNewTurn(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	pongCh := make(chan string, 4)
	receivedCh := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		conn.SetPongHandler(func(appData string) error {
			pongCh <- appData
			return nil
		})
		serverConnCh <- conn
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			receivedCh <- string(payload)
		}
	}))
	defer upstream.Close()

	client, observed := dialDeadlineObservingWebsocket(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))
	serverConn := <-serverConnCh
	defer func() {
		_ = serverConn.Close()
	}()

	exec := NewCodexWebsocketsExecutor(nil)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "liveness-reused-turn"
	defer exec.CloseExecutionSession(sessionID)

	sess := exec.getOrCreateSession(sessionID)
	sess.conn = client
	sess.connCloser = newWebsocketConnectionCloser(client)
	sess.configureConn(client)
	readCh1 := sess.activate(client)
	go exec.readUpstreamLoop(sess, client)

	for _, msg := range []string{
		`{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
		`{"type":"response.completed","response":{"id":"r1","output":[]}}`,
	} {
		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
			t.Fatalf("write %s: %v", msg, errWrite)
		}
		select {
		case ev := <-readCh1:
			if ev.err != nil {
				t.Fatalf("turn 1 error: %v", ev.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for turn 1")
		}
	}
	sess.clearActive(client, readCh1)

	// Confirm readUpstreamLoop is blocked in ReadMessage() on the retained idle connection.
	waitForPingPong(t, serverConn, pongCh, "reuse-sync-0")
	drainDeadlines(observed.deadlines)

	// Simulate starting turn 2 just before the prior idle deadline expires.
	shortDeadline := time.Now().Add(120 * time.Millisecond)
	setTestUnderlyingReadDeadline(t, client, shortDeadline)
	drainDeadlines(observed.deadlines)

	readCh2 := sess.activate(client)
	waitForRenewedDeadline(t, observed.deadlines, shortDeadline.Add(time.Minute))
	if errSend := writeCodexWebsocketMessage(sess, client, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[]}`)); errSend != nil {
		t.Fatalf("write turn 2 request: %v", errSend)
	}
	select {
	case <-receivedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server to receive turn 2 request")
	}

	// Delay upstream's first event past the old remaining idle deadline.
	waitPastDeadline(shortDeadline)

	for _, msg := range []string{
		`{"type":"response.created","response":{"id":"r2","status":"in_progress"}}`,
		`{"type":"response.completed","response":{"id":"r2","output":[]}}`,
	} {
		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
			t.Fatalf("write %s: %v", msg, errWrite)
		}
		select {
		case ev := <-readCh2:
			if ev.err != nil {
				t.Fatalf("turn 2 failed with stale idle deadline: %v", ev.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for turn 2 event")
		}
	}
	sess.clearActive(client, readCh2)
}

func TestCodexWebsocketDuplexLivenessAndIdleCleanup(t *testing.T) {
	t.Run("followup_turn_refreshes_deadline_and_ping_renews", func(t *testing.T) {
		serverConnCh := make(chan *websocket.Conn, 1)
		pongCh := make(chan string, 8)
		turn2ReqCh := make(chan []byte, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			conn.SetPongHandler(func(appData string) error {
				pongCh <- appData
				return nil
			})
			defer func() {
				_ = conn.Close()
			}()
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
			for _, msg := range []string{
				`{"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
				`{"type":"response.completed","response":{"id":"r1","output":[]}}`,
			} {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
					t.Errorf("write turn 1: %v", errWrite)
					return
				}
			}
			serverConnCh <- conn
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			turn2ReqCh <- payload
			// Keep reading so Pong frames are processed until test closes the socket.
			for {
				if _, _, errLoop := conn.ReadMessage(); errLoop != nil {
					return
				}
			}
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		input := make(chan core.WebsocketInput, 4)
		ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

		exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-liveness-followup"
		defer exec.CloseExecutionSession(sessionID)

		auth := &cliproxyauth.Auth{
			ID:       "duplex-auth",
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":    "test-key",
				"base_url":   server.URL,
				"websockets": "true",
			},
		}
		req := core.Request{
			Model:   "gpt-6-astra",
			Payload: []byte(`{"model":"gpt-6-astra","input":[]}`),
		}
		opts := core.Options{
			SourceFormat: sdktranslator.FromString("codex"),
			Metadata:     map[string]any{core.ExecutionSessionMetadataKey: sessionID},
		}

		result, errExec := exec.ExecuteStream(ctx, auth, req, opts)
		if errExec != nil {
			t.Fatalf("ExecuteStream: %v", errExec)
		}

		for _, wantType := range []string{"response.created", "response.completed"} {
			select {
			case chunk, ok := <-result.Chunks:
				if !ok || chunk.Err != nil {
					t.Fatalf("turn 1 chunk error: ok=%t err=%v", ok, chunk.Err)
				}
				if gotType := ojson.GetBytesNoCopy(chunk.Payload, "type").String(); gotType != wantType {
					t.Fatalf("turn 1 event type = %q, want %q", gotType, wantType)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for turn 1 %s", wantType)
			}
		}

		serverConn := <-serverConnCh
		sess := exec.getOrCreateSession(sessionID)
		sess.connMu.Lock()
		clientConn := sess.conn
		sess.connMu.Unlock()
		if clientConn == nil {
			t.Fatal("expected active duplex client conn")
		}

		// Sync idle reader state, then simulate a nearly-expired idle deadline.
		waitForPingPong(t, serverConn, pongCh, "duplex-idle-sync")
		shortDeadline1 := time.Now().Add(120 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, clientConn, shortDeadline1)

		// Send Turn 2 on the duplex socket just before the old idle deadline expires.
		input <- core.WebsocketInput{
			Payload: []byte(`{"type":"response.create","model":"gpt-6-astra","input":[]}`),
		}
		select {
		case <-turn2ReqCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for turn 2 request at upstream")
		}

		waitPastDeadline(shortDeadline1)
		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r2","status":"in_progress","output":[]}}`)); errWrite != nil {
			t.Fatalf("write r2 response.created: %v", errWrite)
		}
		select {
		case chunk, ok := <-result.Chunks:
			if !ok || chunk.Err != nil {
				t.Fatalf("turn 2 response.created failed: ok=%t err=%v", ok, chunk.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for r2 response.created")
		}

		// Verify Ping renews the read deadline during Turn 2.
		waitForPingPong(t, serverConn, pongCh, "duplex-r2-sync")
		shortDeadline2 := time.Now().Add(120 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, clientConn, shortDeadline2)
		waitForPingPong(t, serverConn, pongCh, "duplex-r2-keepalive")
		waitPastDeadline(shortDeadline2)

		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r2","output":[]}}`)); errWrite != nil {
			t.Fatalf("write r2 response.completed: %v", errWrite)
		}
		select {
		case chunk, ok := <-result.Chunks:
			if !ok || chunk.Err != nil {
				t.Fatalf("turn 2 response.completed failed: ok=%t err=%v", ok, chunk.Err)
			}
			if gotType := ojson.GetBytesNoCopy(chunk.Payload, "type").String(); gotType != "response.completed" {
				t.Fatalf("event type = %q, want response.completed", gotType)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for r2 response.completed")
		}
	})

	t.Run("completed_idle_duplex_expires_despite_ping", func(t *testing.T) {
		serverConnCh := make(chan *websocket.Conn, 1)
		pongCh := make(chan string, 8)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			conn.SetPongHandler(func(appData string) error {
				pongCh <- appData
				return nil
			})
			defer func() {
				_ = conn.Close()
			}()
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
			for _, msg := range []string{
				`{"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
				`{"type":"response.completed","response":{"id":"r1","output":[]}}`,
			} {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
					t.Errorf("write turn 1: %v", errWrite)
					return
				}
			}
			serverConnCh <- conn
			for {
				if _, _, errLoop := conn.ReadMessage(); errLoop != nil {
					return
				}
			}
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		input := make(chan core.WebsocketInput, 4)
		ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

		exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-liveness-idle"
		defer exec.CloseExecutionSession(sessionID)

		auth := &cliproxyauth.Auth{
			ID:       "duplex-auth-idle",
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":    "test-key",
				"base_url":   server.URL,
				"websockets": "true",
			},
		}
		req := core.Request{
			Model:   "gpt-6-astra",
			Payload: []byte(`{"model":"gpt-6-astra","input":[]}`),
		}
		opts := core.Options{
			SourceFormat: sdktranslator.FromString("codex"),
			Metadata:     map[string]any{core.ExecutionSessionMetadataKey: sessionID},
		}

		result, errExec := exec.ExecuteStream(ctx, auth, req, opts)
		if errExec != nil {
			t.Fatalf("ExecuteStream: %v", errExec)
		}

		for _, wantType := range []string{"response.created", "response.completed"} {
			select {
			case chunk, ok := <-result.Chunks:
				if !ok || chunk.Err != nil {
					t.Fatalf("turn 1 chunk error: ok=%t err=%v", ok, chunk.Err)
				}
				if gotType := ojson.GetBytesNoCopy(chunk.Payload, "type").String(); gotType != wantType {
					t.Fatalf("turn 1 event type = %q, want %q", gotType, wantType)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for turn 1 %s", wantType)
			}
		}

		serverConn := <-serverConnCh
		sess := exec.getOrCreateSession(sessionID)
		sess.connMu.Lock()
		clientConn := sess.conn
		sess.connMu.Unlock()
		if clientConn == nil {
			t.Fatal("expected active duplex client conn")
		}

		waitForPingPong(t, serverConn, pongCh, "duplex-idle-sync-0")
		shortDeadline := time.Now().Add(120 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, clientConn, shortDeadline)
		waitForPingPong(t, serverConn, pongCh, "duplex-idle-ping-1")

		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("expected timeout chunk before channel close")
			}
			assertTimeoutErr(t, chunk.Err)
		case <-time.After(1500 * time.Millisecond):
			t.Fatal("completed idle duplex session was kept alive past its deadline by upstream ping")
		}
	})

	t.Run("outbound_steer_does_not_extend_silent_upstream", func(t *testing.T) {
		serverConnCh := make(chan *websocket.Conn, 1)
		pongCh := make(chan string, 4)
		steerReceivedCh := make(chan []byte, 4)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			conn.SetPongHandler(func(appData string) error {
				pongCh <- appData
				return nil
			})
			defer func() {
				_ = conn.Close()
			}()
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r-silent","status":"in_progress","output":[]}}`)); errWrite != nil {
				t.Errorf("write response.created: %v", errWrite)
				return
			}
			serverConnCh <- conn
			// Accept incoming steers without ever sending any data or heartbeat back.
			for {
				_, payload, errLoop := conn.ReadMessage()
				if errLoop != nil {
					return
				}
				steerReceivedCh <- payload
			}
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		input := make(chan core.WebsocketInput, 8)
		ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

		exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-liveness-silent-steer"
		defer exec.CloseExecutionSession(sessionID)

		auth := &cliproxyauth.Auth{
			ID:       "duplex-auth-silent-steer",
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":    "test-key",
				"base_url":   server.URL,
				"websockets": "true",
			},
		}
		req := core.Request{
			Model:   "gpt-6-astra",
			Payload: []byte(`{"model":"gpt-6-astra","input":[]}`),
		}
		opts := core.Options{
			SourceFormat: sdktranslator.FromString("codex"),
			Metadata:     map[string]any{core.ExecutionSessionMetadataKey: sessionID},
		}

		result, errExec := exec.ExecuteStream(ctx, auth, req, opts)
		if errExec != nil {
			t.Fatalf("ExecuteStream: %v", errExec)
		}

		select {
		case chunk, ok := <-result.Chunks:
			if !ok || chunk.Err != nil {
				t.Fatalf("response.created chunk error: ok=%t err=%v", ok, chunk.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for response.created")
		}

		serverConn := <-serverConnCh
		sess := exec.getOrCreateSession(sessionID)
		sess.connMu.Lock()
		clientConn := sess.conn
		sess.connMu.Unlock()
		if clientConn == nil {
			t.Fatal("expected active duplex client conn")
		}

		waitForPingPong(t, serverConn, pongCh, "silent-steer-sync")
		shortDeadline := time.Now().Add(180 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, clientConn, shortDeadline)

		// Send steers while the response is already in flight and upstream stays silent.
		// Outbound writes must not extend the read deadline.
		for i := range 2 {
			input <- core.WebsocketInput{
				Payload: []byte(`{"type":"response.steer","previous_response_id":"r-silent","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`),
			}
			select {
			case <-steerReceivedCh:
			case <-time.After(time.Second):
				t.Fatalf("timed out waiting for steer %d at upstream", i)
			}
		}

		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("expected timeout chunk before channel close")
			}
			assertTimeoutErr(t, chunk.Err)
		case <-time.After(1500 * time.Millisecond):
			t.Fatal("outbound steers extended read deadline on silent upstream")
		}
	})

	t.Run("completed_upstream_expires_under_downstream_backpressure", func(t *testing.T) {
		serverConnCh := make(chan *websocket.Conn, 1)
		pongCh := make(chan string, 8)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			conn.SetPongHandler(func(appData string) error {
				pongCh <- appData
				return nil
			})
			defer func() {
				_ = conn.Close()
			}()
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r-bp","status":"in_progress","output":[]}}`)); errWrite != nil {
				t.Errorf("write response.created: %v", errWrite)
				return
			}
			serverConnCh <- conn
			for {
				if _, _, errLoop := conn.ReadMessage(); errLoop != nil {
					return
				}
			}
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		input := make(chan core.WebsocketInput, 4)
		ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

		exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "duplex-liveness-backpressure"
		defer exec.CloseExecutionSession(sessionID)

		auth := &cliproxyauth.Auth{
			ID:       "duplex-auth-bp",
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":    "test-key",
				"base_url":   server.URL,
				"websockets": "true",
			},
		}
		req := core.Request{
			Model:   "gpt-6-astra",
			Payload: []byte(`{"model":"gpt-6-astra","input":[]}`),
		}
		opts := core.Options{
			SourceFormat: sdktranslator.FromString("codex"),
			Metadata:     map[string]any{core.ExecutionSessionMetadataKey: sessionID},
		}

		result, errExec := exec.ExecuteStream(ctx, auth, req, opts)
		if errExec != nil {
			t.Fatalf("ExecuteStream: %v", errExec)
		}

		// Read response.created, then STOP consuming result.Chunks so the duplex
		// forwarding goroutine blocks trying to deliver the delta downstream.
		select {
		case chunk, ok := <-result.Chunks:
			if !ok || chunk.Err != nil {
				t.Fatalf("response.created chunk error: ok=%t err=%v", ok, chunk.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for response.created")
		}

		serverConn := <-serverConnCh
		sess := exec.getOrCreateSession(sessionID)
		sess.connMu.Lock()
		clientConn := sess.conn
		sess.connMu.Unlock()
		if clientConn == nil {
			t.Fatal("expected active duplex client conn")
		}

		// Send a delta (which blocks duplex forwarding on unbuffered result.Chunks)
		// followed by response.completed (read and queued by readUpstreamLoop).
		for _, msg := range []string{
			`{"type":"response.output_text.delta","delta":"blocked-downstream"}`,
			`{"type":"response.completed","response":{"id":"r-bp","output":[]}}`,
		} {
			if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
				t.Fatalf("write %s: %v", msg, errWrite)
			}
		}

		// Sync proves readUpstreamLoop already consumed response.completed and re-entered ReadMessage().
		waitForPingPong(t, serverConn, pongCh, "bp-sync-0")
		shortDeadline := time.Now().Add(120 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, clientConn, shortDeadline)

		// While downstream is still stalled and has not consumed the delta or completion,
		// upstream pings must NOT keep the already-completed upstream connection alive.
		waitForPingPong(t, serverConn, pongCh, "bp-ping-1")

		select {
		case errDisc := <-sess.upstreamDisconnectCh:
			assertTimeoutErr(t, errDisc)
		case <-time.After(1500 * time.Millisecond):
			t.Fatal("completed upstream connection was kept alive under downstream backpressure")
		}
	})
}

func TestCodexWebsocketUnsolicitedPongHeartbeatRespectsTurnState(t *testing.T) {
	t.Run("active_session_renews_on_unsolicited_pong", func(t *testing.T) {
		serverConnCh := make(chan *websocket.Conn, 1)
		pongCh := make(chan string, 4)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			conn.SetPongHandler(func(appData string) error {
				pongCh <- appData
				return nil
			})
			serverConnCh <- conn
			for {
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					return
				}
			}
		}))
		defer upstream.Close()

		client, observed := dialDeadlineObservingWebsocket(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))
		serverConn := <-serverConnCh
		defer func() {
			_ = serverConn.Close()
		}()

		exec := NewCodexWebsocketsExecutor(nil)
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "liveness-active-pong"
		defer exec.CloseExecutionSession(sessionID)

		sess := exec.getOrCreateSession(sessionID)
		sess.conn = client
		sess.connCloser = newWebsocketConnectionCloser(client)
		sess.configureConn(client)
		readCh := sess.activate(client)
		go exec.readUpstreamLoop(sess, client)

		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r-pong","status":"in_progress"}}`)); errWrite != nil {
			t.Fatalf("write response.created: %v", errWrite)
		}
		select {
		case ev := <-readCh:
			if ev.err != nil {
				t.Fatalf("unexpected error: %v", ev.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for response.created")
		}

		waitForPingPong(t, serverConn, pongCh, "pong-sync-0")
		drainDeadlines(observed.deadlines)

		shortDeadline := time.Now().Add(120 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, client, shortDeadline)
		drainDeadlines(observed.deadlines)

		if errPong := serverConn.WriteControl(websocket.PongMessage, []byte("unsolicited-pong"), time.Now().Add(2*time.Second)); errPong != nil {
			t.Fatalf("write unsolicited pong: %v", errPong)
		}
		// Explicitly confirm the unsolicited Pong itself renewed the read deadline.
		waitForRenewedDeadline(t, observed.deadlines, shortDeadline.Add(time.Minute))
		waitPastDeadline(shortDeadline)

		if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r-pong","output":[]}}`)); errWrite != nil {
			t.Fatalf("write response.completed: %v", errWrite)
		}
		select {
		case ev := <-readCh:
			if ev.err != nil {
				t.Fatalf("active response failed after unsolicited pong: %v", ev.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for response.completed")
		}
		sess.clearActive(client, readCh)
	})

	t.Run("idle_session_expires_despite_unsolicited_pong", func(t *testing.T) {
		serverConnCh := make(chan *websocket.Conn, 1)
		pongCh := make(chan string, 4)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			conn.SetPongHandler(func(appData string) error {
				pongCh <- appData
				return nil
			})
			serverConnCh <- conn
			for {
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					return
				}
			}
		}))
		defer upstream.Close()

		client, observed := dialDeadlineObservingWebsocket(t, "ws"+strings.TrimPrefix(upstream.URL, "http"))
		serverConn := <-serverConnCh
		defer func() {
			_ = serverConn.Close()
		}()

		exec := NewCodexWebsocketsExecutor(nil)
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		const sessionID = "liveness-idle-pong"
		defer exec.CloseExecutionSession(sessionID)

		sess := exec.getOrCreateSession(sessionID)
		sess.conn = client
		sess.connCloser = newWebsocketConnectionCloser(client)
		sess.configureConn(client)
		readCh := sess.activate(client)
		go exec.readUpstreamLoop(sess, client)

		for _, msg := range []string{
			`{"type":"response.created","response":{"id":"r-pong-idle","status":"in_progress"}}`,
			`{"type":"response.completed","response":{"id":"r-pong-idle","output":[]}}`,
		} {
			if errWrite := serverConn.WriteMessage(websocket.TextMessage, []byte(msg)); errWrite != nil {
				t.Fatalf("write %s: %v", msg, errWrite)
			}
			select {
			case ev := <-readCh:
				if ev.err != nil {
					t.Fatalf("unexpected error: %v", ev.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for turn 1")
			}
		}
		sess.clearActive(client, readCh)

		waitForPingPong(t, serverConn, pongCh, "pong-idle-sync-0")
		drainDeadlines(observed.deadlines)

		shortDeadline := time.Now().Add(120 * time.Millisecond)
		setTestUnderlyingReadDeadline(t, client, shortDeadline)
		drainDeadlines(observed.deadlines)

		if errPong := serverConn.WriteControl(websocket.PongMessage, []byte("unsolicited-idle-pong"), time.Now().Add(2*time.Second)); errPong != nil {
			t.Fatalf("write unsolicited pong: %v", errPong)
		}

		select {
		case errDisc := <-sess.upstreamDisconnectCh:
			assertTimeoutErr(t, errDisc)
		case <-time.After(1500 * time.Millisecond):
			t.Fatal("completed idle session was kept alive past its deadline by unsolicited pong")
		}
	})
}
