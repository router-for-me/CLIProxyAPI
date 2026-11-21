package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const codexHTTPMessagesLimit = 256

var errCodexHTTPMessagesParentMissing = errors.New("Codex rejected the generated previous_response_id")

func codexHTTPMessagesParentMissing(payload []byte) bool {
	event := helps.CodexResponseEventType(payload)
	if event != "error" && event != "response.failed" {
		return false
	}
	status := codexWebsocketErrorStatus(payload)
	if status != http.StatusBadRequest && status != http.StatusNotFound && status != 0 {
		return false
	}
	for _, path := range []string{"error.code", "body.error.code", "response.error.code", "code"} {
		if ojson.GetBytesNoCopy(payload, path).String() == "previous_response_not_found" {
			return true
		}
	}
	return false
}

func codexHTTPMessagesUpgradeUnsupported(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	status := resp.StatusCode
	return status >= 200 && status < 300 || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented
}

// The lease fields are protected by the WebSocket store mutex; the continuation
// fields are used only by the lease owner and committed before the stream closes.
type codexHTTPMessagesSession struct {
	closed       bool     // protected by the connection mutex
	handshake    [32]byte // protected by the connection mutex
	busy         bool
	continuation codexHTTPMessagesContinuation
}

type codexHTTPMessagesContinuation struct {
	conn     *websocket.Conn
	snapshot helps.CodexMessagesContinuation
}

func (e *CodexWebsocketsExecutor) serveHTTPMessages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error, bool) {
	if e.cfg == nil || !e.cfg.Codex.ServeHTTPMessagesOverWebsockets || !codexWebsocketsEnabled(auth) ||
		auth.ID == "" || opts.SourceFormat != sdktranslator.FormatClaude || cliproxyexecutor.ResponseFormatOrSource(opts) != sdktranslator.FormatClaude ||
		opts.Alt != "" || opts.ExecutionLifecycle != nil || e.cfg.Home.Enabled ||
		helps.PayloadRequestPath(opts) != "/v1/messages" || opts.Metadata[cliproxyexecutor.RequestSourceMetadataKey] == cliproxyexecutor.InternalModelCallbackSource {
		return nil, nil, false
	}
	scope, _ := opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	// Do not use prompt_cache_key, a content-derived LCP ID, or an arbitrary
	// user ID as a connection identity; those can be shared across chats.
	sid := coresession.ClaudeMetadataSessionID(payload)
	for _, header := range []string{helps.ClaudeCodeSessionHeader, "X-Session-Id", "Session-Id", "Session_id"} {
		if explicit := coresession.NormalizeExplicitID(helps.HeaderValueCaseInsensitive(opts.Headers, header)); explicit != "" {
			sid = explicit
			break
		}
	}
	if scope == "" || sid == "" {
		return nil, nil, false
	}
	agent := coresession.ClaudeAgentID(opts.Headers, payload)
	key := "http-messages:" + codexHTTPMessagesSessionKey(scope, sid, agent, auth.ID, req.Model)
	sess, release := e.leaseHTTPMessagesSession(key, auth.ID)
	if sess == nil {
		return nil, nil, false
	}
	res, err := e.executeStream(ctx, auth, req, opts, sess, release)
	if cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) || errors.Is(err, errCodexHTTPMessagesParentMissing) {
		return nil, nil, false // HTTP sends the original full history after a definite rejection or no write.
	}
	return res, err, true
}

func codexHTTPMessagesSessionKey(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		_, _ = h.Write(strconv.AppendInt(nil, int64(len(v)), 10))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (e *CodexWebsocketsExecutor) leaseHTTPMessagesSession(key, authID string) (*codexWebsocketSession, func()) {
	store := e.sessionStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions == nil {
		store.sessions = make(map[string]*codexWebsocketSession)
	}
	sess := store.sessions[key]
	if sess == nil {
		count := 0
		for _, other := range store.sessions {
			if other != nil && other.httpMessages != nil {
				count++
			}
		}
		if count >= codexHTTPMessagesLimit {
			return nil, nil
		}
		sess = &codexWebsocketSession{sessionID: key, authID: authID, upstreamDisconnectCh: make(chan error, 1), httpMessages: &codexHTTPMessagesSession{}}
		store.sessions[key] = sess
	}
	state := sess.httpMessages
	if state == nil || state.busy {
		return nil, nil
	}
	state.busy = true
	return sess, func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.sessions[key] != sess {
			return
		}
		sess.connMu.Lock()
		sess.turnContext = nil
		open := sess.conn != nil
		sess.connMu.Unlock()
		if !open {
			delete(store.sessions, key)
			return
		}
		state.busy = false
	}
}

// A disconnected idle session no longer needs a pool entry. If a leased turn
// still owns it, its release is responsible for removing the dead entry.
func (e *CodexWebsocketsExecutor) removeDisconnectedHTTPMessagesSession(sess *codexWebsocketSession) {
	store := e.sessionStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions[sess.sessionID] != sess || sess.httpMessages.busy {
		return
	}
	sess.connMu.Lock()
	defer sess.connMu.Unlock()
	if sess.conn == nil {
		delete(store.sessions, sess.sessionID)
	}
}

func (s *codexWebsocketSession) matchesHTTPMessagesHandshake(conn *websocket.Conn, digest [32]byte) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn == conn && !s.httpMessages.closed && s.httpMessages.handshake == digest
}
