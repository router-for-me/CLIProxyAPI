package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
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

func TestCodexWebsocketsEnsureUpstreamConnRebindsWhenAuthChanges(t *testing.T) {
	acceptedAuth := make(chan string, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		acceptedAuth <- r.Header.Get("Authorization")
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	})
	serverA := httptest.NewServer(handler)
	defer serverA.Close()

	wsURLA := "ws" + strings.TrimPrefix(serverA.URL, "http")
	firstHeaders := http.Header{"Authorization": []string{"Bearer auth-a-token"}}
	first, _, errDial := websocket.DefaultDialer.Dial(wsURLA, firstHeaders)
	if errDial != nil {
		t.Fatalf("dial first websocket: %v", errDial)
	}
	if got := waitAcceptedAuth(t, acceptedAuth); got != "Bearer auth-a-token" {
		t.Fatalf("first authorization = %q", got)
	}
	sess := &codexWebsocketSession{
		sessionID:            "rotation-session",
		conn:                 first,
		readerConn:           first,
		wsURL:                wsURLA,
		authID:               "auth-a",
		upstreamDisconnectCh: make(chan error, 1),
	}
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	secondHeaders := http.Header{"Authorization": []string{"Bearer auth-b-token"}}

	second, _, errRebind := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-b", wsURLA, secondHeaders)
	if errRebind != nil {
		t.Fatalf("rebind websocket: %v", errRebind)
	}
	defer func() { _ = second.Close() }()
	if second == first {
		t.Fatal("auth change reused stale websocket connection")
	}
	if got := waitAcceptedAuth(t, acceptedAuth); got != "Bearer auth-b-token" {
		t.Fatalf("rebound authorization = %q", got)
	}

	active := make(chan codexWebsocketRead, 1)
	sess.setActiveForConn(second, active)
	staleReaderDone := make(chan struct{})
	go func() {
		exec.readUpstreamLoop(sess, first)
		close(staleReaderDone)
	}()
	select {
	case <-staleReaderDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale websocket reader")
	}
	sess.activeMu.Lock()
	activeConn := sess.activeConn
	activeCh := sess.activeCh
	sess.activeMu.Unlock()
	if activeConn != second || activeCh != active {
		t.Fatal("stale reader cleared rebound websocket active channel")
	}
	sess.connMu.Lock()
	boundAuthID := sess.authID
	sess.connMu.Unlock()
	if boundAuthID != "auth-b" {
		t.Fatalf("session auth ID = %q, want auth-b", boundAuthID)
	}
	sess.clearActiveForConn(second, active)
	select {
	case errDisconnect := <-sess.upstreamDisconnectCh:
		t.Fatalf("intentional auth rebind signaled session disconnect: %v", errDisconnect)
	default:
	}
}

func TestCodexWebsocketsEnsureUpstreamConnRebindsWhenURLChanges(t *testing.T) {
	acceptedAuth := make(chan string, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		acceptedAuth <- r.Header.Get("Authorization")
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	})
	serverA := httptest.NewServer(handler)
	defer serverA.Close()
	serverB := httptest.NewServer(handler)
	defer serverB.Close()
	wsURLA := "ws" + strings.TrimPrefix(serverA.URL, "http")
	wsURLB := "ws" + strings.TrimPrefix(serverB.URL, "http")
	headers := http.Header{"Authorization": []string{"Bearer auth-b-token"}}
	first, _, errDial := websocket.DefaultDialer.Dial(wsURLA, headers)
	if errDial != nil {
		t.Fatalf("dial first websocket: %v", errDial)
	}
	if got := waitAcceptedAuth(t, acceptedAuth); got != "Bearer auth-b-token" {
		t.Fatalf("first authorization = %q", got)
	}
	sess := &codexWebsocketSession{
		sessionID:            "url-rotation-session",
		conn:                 first,
		readerConn:           first,
		wsURL:                wsURLA,
		authID:               "auth-b",
		upstreamDisconnectCh: make(chan error, 1),
	}
	exec := NewCodexWebsocketsExecutor(&config.Config{})

	second, _, errRebind := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-b", wsURLB, headers)
	if errRebind != nil {
		t.Fatalf("rebind websocket URL: %v", errRebind)
	}
	defer func() { _ = second.Close() }()
	if second == first {
		t.Fatal("URL change reused stale websocket connection")
	}
	if got := waitAcceptedAuth(t, acceptedAuth); got != "Bearer auth-b-token" {
		t.Fatalf("rebound authorization = %q", got)
	}
	active := make(chan codexWebsocketRead, 1)
	sess.setActiveForConn(second, active)
	staleReaderDone := make(chan struct{})
	go func() {
		exec.readUpstreamLoop(sess, first)
		close(staleReaderDone)
	}()
	select {
	case <-staleReaderDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale URL reader")
	}
	sess.connMu.Lock()
	boundWSURL := sess.wsURL
	sess.connMu.Unlock()
	if boundWSURL != wsURLB {
		t.Fatalf("session websocket URL = %q, want %q", boundWSURL, wsURLB)
	}
	sess.activeMu.Lock()
	activeConn := sess.activeConn
	activeCh := sess.activeCh
	sess.activeMu.Unlock()
	if activeConn != second || activeCh != active {
		t.Fatal("stale URL reader cleared rebound websocket active channel")
	}
	sess.clearActiveForConn(second, active)
	select {
	case errDisconnect := <-sess.upstreamDisconnectCh:
		t.Fatalf("intentional URL rebind signaled session disconnect: %v", errDisconnect)
	default:
	}
}

func TestCodexWebsocketsEnsureUpstreamConnRejectsDialAfterSessionClose(t *testing.T) {
	dialStarted := make(chan struct{})
	allowUpgrade := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(dialStarted)
		<-allowUpgrade
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	sess := &codexWebsocketSession{
		sessionID:            "closing-session",
		upstreamDisconnectCh: make(chan error, 1),
	}
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	type dialResult struct {
		conn *websocket.Conn
		err  error
	}
	resultCh := make(chan dialResult, 1)
	go func() {
		conn, _, errDial := exec.ensureUpstreamConn(context.Background(), nil, sess, "auth-b", wsURL, nil)
		resultCh <- dialResult{conn: conn, err: errDial}
	}()

	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for websocket dial")
	}
	closeCodexWebsocketSession(sess, "session_closed")
	close(allowUpgrade)
	var result dialResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rejected websocket dial")
	}
	if result.conn != nil {
		_ = result.conn.Close()
		t.Fatal("closed session installed rebound websocket connection")
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "closed during websocket dial") {
		t.Fatalf("dial error = %v, want closed-session error", result.err)
	}
	sess.connMu.Lock()
	installed := sess.conn
	closed := sess.closed
	sess.connMu.Unlock()
	if installed != nil || !closed {
		t.Fatalf("closed session state = conn:%v closed:%v", installed, closed)
	}
}

func TestCodexWebsocketsSendFailureOwnsDetachBeforeStaleReader(t *testing.T) {
	accepted := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		accepted <- struct{}{}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	<-accepted
	sess := &codexWebsocketSession{
		sessionID:            "send-reader-race",
		conn:                 conn,
		readerConn:           conn,
		wsURL:                wsURL,
		authID:               "auth-a",
		upstreamDisconnectCh: make(chan error, 1),
	}
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	_ = conn.Close()

	sess.writeMu.Lock()
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- sess.writeMessage(conn, websocket.TextMessage, []byte(`{"type":"response.create"}`))
	}()
	waitRecoverableWrite(t, sess, conn)
	readerDone := make(chan struct{})
	go func() {
		exec.readUpstreamLoop(sess, conn)
		close(readerDone)
	}()
	sess.writeMu.Unlock()
	select {
	case errWrite := <-writeResult:
		if errWrite == nil {
			t.Fatal("closed websocket write unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for websocket write failure")
	}
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale reader failure")
	}
	assertNoUpstreamDisconnect(t, sess)
}

func TestCodexWebsocketsSessionCloseDoesNotWaitForBlockedWrite(t *testing.T) {
	accepted := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		accepted <- struct{}{}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	<-accepted
	sess := &codexWebsocketSession{
		sessionID:            "close-blocked-write",
		conn:                 conn,
		readerConn:           conn,
		wsURL:                wsURL,
		authID:               "auth-a",
		upstreamDisconnectCh: make(chan error, 1),
	}
	sess.writeMu.Lock()
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- sess.writeMessage(conn, websocket.TextMessage, []byte(`{"type":"response.create"}`))
	}()
	waitRecoverableWrite(t, sess, conn)
	closeDone := make(chan struct{})
	go func() {
		closeCodexWebsocketSession(sess, "session_closed")
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		sess.writeMu.Unlock()
		t.Fatal("session close waited for blocked websocket write")
	}
	sess.writeMu.Unlock()
	select {
	case errWrite := <-writeResult:
		if errWrite == nil {
			t.Fatal("closed session write unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for closed-session write failure")
	}
	sess.connMu.Lock()
	closed := sess.closed
	installed := sess.conn
	sess.connMu.Unlock()
	if !closed || installed != nil {
		t.Fatalf("session close state = closed:%v conn:%v", closed, installed)
	}
}

func TestCodexWebsocketsExecuteRebindsActiveReaderAfterSendRetry(t *testing.T) {
	fixture := newCodexWebsocketSendRetryFixture(t, "unary-retry")
	defer fixture.close()

	if _, errExecute := fixture.exec.Execute(context.Background(), fixture.auth, fixture.request, fixture.options); errExecute != nil {
		t.Fatalf("Execute() retry error = %v", errExecute)
	}
	if got := fixture.acceptedConnections(); got != 2 {
		t.Fatalf("accepted websocket connections = %d, want 2", got)
	}
	assertNoUpstreamDisconnect(t, fixture.session)
}

func TestCodexWebsocketsExecuteStreamRebindsActiveReaderAfterSendRetry(t *testing.T) {
	fixture := newCodexWebsocketSendRetryFixture(t, "stream-retry")
	defer fixture.close()
	fixture.options.ResponseFormat = sdktranslator.FromString("openai-response")
	ctx, cancel := context.WithCancel(cliproxyexecutor.WithDownstreamWebsocket(context.Background()))
	defer cancel()

	result, errExecute := fixture.exec.ExecuteStream(ctx, fixture.auth, fixture.request, fixture.options)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() retry error = %v", errExecute)
	}
	want := []byte(`{"type":"response.output_text.delta","delta":"retry-ok"}`)
	sawDelta := false
	deadline := time.After(time.Second)
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				if !sawDelta {
					t.Fatal("retry stream closed without expected delta")
				}
				goto drained
			}
			if chunk.Err != nil {
				t.Fatalf("retry stream chunk error = %v", chunk.Err)
			}
			if bytes.Equal(bytes.TrimSpace(chunk.Payload), want) {
				sawDelta = true
			}
		case <-deadline:
			t.Fatal("timed out draining retry stream")
		}
	}

drained:
	if got := fixture.acceptedConnections(); got != 2 {
		t.Fatalf("accepted websocket connections = %d, want 2", got)
	}
	assertNoUpstreamDisconnect(t, fixture.session)
}

type codexWebsocketSendRetryFixture struct {
	exec        *CodexWebsocketsExecutor
	auth        *cliproxyauth.Auth
	request     cliproxyexecutor.Request
	options     cliproxyexecutor.Options
	server      *httptest.Server
	accepted    chan int
	connections *atomic.Int32
	session     *codexWebsocketSession
	release     chan struct{}
}

func newCodexWebsocketSendRetryFixture(t *testing.T, sessionID string) *codexWebsocketSendRetryFixture {
	t.Helper()
	accepted := make(chan int, 2)
	connections := &atomic.Int32{}
	release := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		connectionNumber := int(connections.Add(1))
		accepted <- connectionNumber
		defer func() { _ = conn.Close() }()
		if connectionNumber == 1 {
			_, _, _ = conn.ReadMessage()
			return
		}
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}
		delta := []byte(`{"type":"response.output_text.delta","delta":"retry-ok"}`)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp-retry","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		_ = conn.WriteMessage(websocket.TextMessage, delta)
		_ = conn.WriteMessage(websocket.TextMessage, completed)
		<-release
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/responses"
	first, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		server.Close()
		t.Fatalf("dial stale websocket: %v", errDial)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		_ = first.Close()
		server.Close()
		t.Fatal("timed out waiting for stale websocket connection")
	}
	_ = first.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	exec.store = &codexWebsocketSessionStore{sessions: map[string]*codexWebsocketSession{}}
	auth := &cliproxyauth.Auth{
		ID:       "auth-retry",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "retry-token",
			"base_url": server.URL,
		},
	}
	session := &codexWebsocketSession{
		sessionID:            sessionID,
		conn:                 first,
		readerConn:           first,
		wsURL:                wsURL,
		authID:               auth.ID,
		upstreamDisconnectCh: make(chan error, 1),
	}
	exec.store.sessions[sessionID] = session
	return &codexWebsocketSendRetryFixture{
		exec: exec,
		auth: auth,
		request: cliproxyexecutor.Request{
			Model:   "gpt-5-codex",
			Payload: []byte(`{"model":"gpt-5-codex","input":[{"role":"user","content":"retry"}]}`),
		},
		options: cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Metadata: map[string]any{
				cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
			},
		},
		server:      server,
		accepted:    accepted,
		connections: connections,
		session:     session,
		release:     release,
	}
}

func (f *codexWebsocketSendRetryFixture) acceptedConnections() int {
	return int(f.connections.Load())
}

func (f *codexWebsocketSendRetryFixture) close() {
	if f == nil {
		return
	}
	close(f.release)
	f.exec.CloseExecutionSession(executionSessionIDFromOptions(f.options))
	f.server.Close()
}

func assertNoUpstreamDisconnect(t *testing.T, sess *codexWebsocketSession) {
	t.Helper()
	select {
	case errDisconnect := <-sess.upstreamDisconnectCh:
		t.Fatalf("successful websocket retry signaled session disconnect: %v", errDisconnect)
	default:
	}
}

func waitRecoverableWrite(t *testing.T, sess *codexWebsocketSession, conn *websocket.Conn) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		sess.connMu.Lock()
		marked := sess.recoverableWriteConn == conn
		sess.connMu.Unlock()
		if marked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for recoverable write marker")
		}
		runtime.Gosched()
	}
}

func waitAcceptedAuth(t *testing.T, accepted <-chan string) string {
	t.Helper()
	select {
	case auth := <-accepted:
		return auth
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for websocket handshake")
		return ""
	}
}
