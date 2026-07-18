package executor

import (
	"context"
	"net/http"

	"github.com/gorilla/websocket"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (e *CodexWebsocketsExecutor) ensureUpstreamConn(ctx context.Context, auth *cliproxyauth.Auth, sess *codexWebsocketSession, authID string, wsURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	return e.ensureUpstreamConnWithReader(ctx, auth, sess, authID, wsURL, headers, true)
}

func (e *CodexWebsocketsExecutor) ensureUpstreamConnForRequest(ctx context.Context, auth *cliproxyauth.Auth, sess *codexWebsocketSession, authID string, wsURL string, headers http.Header) (*websocket.Conn, *http.Response, chan codexWebsocketRead, error) {
	conn, resp, err := e.ensureUpstreamConnWithReader(ctx, auth, sess, authID, wsURL, headers, false)
	if err != nil || sess == nil {
		return conn, resp, nil, err
	}
	return conn, resp, e.activateCodexWebsocketSessionConn(sess, conn), nil
}

// activateCodexWebsocketSessionConn registers the request before starting a
// new reader so an immediate upstream close is delivered to that request.
func (e *CodexWebsocketsExecutor) activateCodexWebsocketSessionConn(sess *codexWebsocketSession, conn *websocket.Conn) chan codexWebsocketRead {
	if e == nil || sess == nil || conn == nil {
		return nil
	}

	ch := sess.activate(conn)
	sess.connMu.Lock()
	startReader := sess.conn == conn && sess.readerConn != conn
	if startReader {
		sess.readerConn = conn
	}
	sess.connMu.Unlock()

	if startReader {
		sess.configureConn(conn)
		go e.readUpstreamLoop(sess, conn)
	}
	return ch
}
