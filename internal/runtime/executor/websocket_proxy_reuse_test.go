package executor

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebsocketSessionIsolatesReusableConnectionByProxy(t *testing.T) {
	conn := &websocket.Conn{}
	closer := newWebsocketConnectionCloser(conn)
	sess := &codexWebsocketSession{
		authID:     "auth-1",
		wsURL:      "wss://upstream.example/v1",
		proxyURL:   "http://proxy-a.example:8081",
		conn:       conn,
		connCloser: closer,
	}

	if got, _ := existingWebsocketSessionConn(sess, "auth-1", sess.wsURL, "http://proxy-b.example:8082"); got != nil {
		t.Fatal("reused websocket after proxy_url changed")
	}
	if got, _ := existingWebsocketSessionConn(sess, "auth-1", sess.wsURL, ""); got != nil {
		t.Fatal("reused proxied websocket after proxy override was removed")
	}
	if got, _ := existingWebsocketSessionConn(sess, "auth-1", sess.wsURL, sess.proxyURL); got == nil {
		t.Fatal("did not reuse websocket for the same proxy")
	}
	if !websocketSessionTargetChanged(sess, "auth-1", sess.wsURL, "http://proxy-b.example:8082") {
		t.Fatal("proxy change was not treated as a websocket target change")
	}

	detached, detachedCloser, _, _, _ := detachMismatchedWebsocketSessionConn(sess, "auth-1", sess.wsURL, "")
	if detached == nil || detachedCloser == nil {
		t.Fatal("removing the proxy override did not detach the websocket")
	}
	if sess.conn != nil {
		t.Fatal("detached websocket remained attached")
	}
}
