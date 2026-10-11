// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements a Codex executor that uses the Responses API WebSocket transport.
package executor

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// CodexWebsocketsExecutor executes Codex Responses requests using a WebSocket transport.
//
// It preserves the existing CodexExecutor HTTP implementation as a fallback for endpoints
// not available over WebSocket (e.g. /responses/compact) and for websocket upgrade failures.
type CodexWebsocketsExecutor struct {
	*CodexExecutor

	store *codexWebsocketSessionStore
}

func NewCodexWebsocketsExecutor(cfg *config.Config) *CodexWebsocketsExecutor {
	return &CodexWebsocketsExecutor{
		CodexExecutor: NewCodexExecutor(cfg),
		store:         globalCodexWebsocketSessionStore,
	}
}

// CodexAutoExecutor routes Codex requests to the websocket transport only when:
//  1. The downstream transport is websocket, and
//  2. The selected auth enables websockets.
//
// For non-websocket downstream requests, it always uses the legacy HTTP implementation.
type CodexAutoExecutor struct {
	httpExec *CodexExecutor
	wsExec   *CodexWebsocketsExecutor
}

func NewCodexAutoExecutor(cfg *config.Config) *CodexAutoExecutor {
	return &CodexAutoExecutor{
		httpExec: NewCodexExecutor(cfg),
		wsExec:   NewCodexWebsocketsExecutor(cfg),
	}
}

func (e *CodexAutoExecutor) Identifier() string { return "codex" }

func (e *CodexAutoExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if e == nil || e.httpExec == nil {
		return nil
	}
	return e.httpExec.PrepareRequest(req, auth)
}

func (e *CodexAutoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.HttpRequest(ctx, auth, req)
}

func (e *CodexAutoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex auto executor: executor is nil")
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) && codexWebsocketsEnabled(auth) {
		return e.wsExec.Execute(ctx, auth, req, opts)
	}
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return cliproxyexecutor.Response{}, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
	}
	return e.httpExec.Execute(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return nil, fmt.Errorf("codex auto executor: executor is nil")
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) && codexWebsocketsEnabled(auth) {
		return e.wsExec.ExecuteStream(ctx, auth, req, opts)
	}
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return nil, cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
	}
	return e.httpExec.ExecuteStream(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if e == nil || e.httpExec == nil {
		return nil, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.Refresh(ctx, auth)
}

func (e *CodexAutoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.httpExec == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex auto executor: http executor is nil")
	}
	return e.httpExec.CountTokens(ctx, auth, req, opts)
}

// CloseExecutionSessionsForAuth releases sessions belonging to a removed auth.
func (e *CodexAutoExecutor) CloseExecutionSessionsForAuth(authID string) {
	if e != nil && e.wsExec != nil {
		e.wsExec.CloseExecutionSessionsForAuth(authID)
	}
}

func (e *CodexAutoExecutor) CloseExecutionSession(sessionID string) {
	if e == nil || e.wsExec == nil {
		return
	}
	e.wsExec.CloseExecutionSession(sessionID)
}

func (e *CodexAutoExecutor) UpstreamDisconnectChan(sessionID string) <-chan error {
	if e == nil || e.wsExec == nil {
		return nil
	}
	return e.wsExec.UpstreamDisconnectChan(sessionID)
}

func codexWebsocketsEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

// SupportsApplyPatch requires both selectable transports to support the tool.
func (e *CodexAutoExecutor) SupportsApplyPatch() bool {
	return e != nil && e.httpExec != nil && e.wsExec != nil && e.httpExec.SupportsApplyPatch() && e.wsExec.SupportsApplyPatch()
}

// InterruptExecutionSession forwards a response.interrupt control frame on the
// current upstream socket. It does not dial, replay, or select another credential.
func (e *CodexAutoExecutor) InterruptExecutionSession(ctx context.Context, sessionID string, payload []byte) error {
	if e == nil || e.wsExec == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	return e.wsExec.InterruptExecutionSession(ctx, sessionID, payload)
}

// InterruptExecutionSession writes the original interrupt payload to the session
// socket captured for this execution. Payload rules and response.create defaults
// must not rewrite response_id, mode, or extension fields.
func (e *CodexWebsocketsExecutor) InterruptExecutionSession(ctx context.Context, sessionID string, payload []byte) error {
	if e == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	sess := store.sessions[sessionID]
	store.mu.Unlock()
	if sess == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	sess.connMu.Lock()
	conn := sess.conn
	authID := sess.authID
	sess.connMu.Unlock()
	if conn == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	// Account checks may call back into the auth manager; do not hold session
	// locks across them. Take the lifecycle snapshot only after they finish.
	authEnabled := cliproxyexecutor.WebsocketAuthEnabled(ctx, authID)
	sess.connMu.Lock()
	if sess.conn != conn {
		sess.connMu.Unlock()
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	sess.activeMu.Lock()
	_, alreadyTerminal := sess.terminalResponseIDs[gjson.GetBytes(payload, "response_id").String()]
	alreadyTerminal = alreadyTerminal && sess.responseStateConn == conn
	active := sess.activeConn == conn && sess.activeCh != nil
	var disconnectErr error
	if sess.terminalConn == conn {
		disconnectErr = sess.terminalErr
	}
	// Terminal state and the active channel must belong to one snapshot:
	// completion can otherwise land between the checks and produce a stale 400.
	sess.activeMu.Unlock()
	sess.connMu.Unlock()
	if disconnectErr != nil {
		return disconnectErr
	}
	if alreadyTerminal {
		log.WithField("outcome", "already_terminal").Debug("codex websockets: response.interrupt handled")
		return nil
	}
	// A retained socket from an earlier turn is not the current upstream.
	// HTTP turns must fall through to local cancellation instead.
	if !active {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	if !authEnabled {
		return fmt.Errorf("websocket credential is no longer enabled")
	}
	if errWrite := writeCodexWebsocketMessage(sess, conn, payload); errWrite != nil {
		return errWrite
	}
	log.WithField("outcome", "forwarded").Debug("codex websockets: response.interrupt handled")
	return nil
}
