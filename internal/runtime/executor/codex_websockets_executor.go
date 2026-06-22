// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements a Codex executor that uses the Responses API WebSocket transport.
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/proxy"
)

const (
	codexResponsesWebsocketBetaHeaderValue = "responses_websockets=2026-02-06"
	codexResponsesWebsocketIdleTimeout     = 5 * time.Minute
	codexResponsesWebsocketHandshakeTO     = 30 * time.Second
	codexResponsesWebsocketSendRetryLimit  = 2
)

// CodexWebsocketsExecutor executes Codex Responses requests using a WebSocket transport.
//
// It preserves the existing CodexExecutor HTTP implementation as a fallback for endpoints
// not available over WebSocket (e.g. /responses/compact) and for websocket upgrade failures.
type CodexWebsocketsExecutor struct {
	*CodexExecutor

	store *codexWebsocketSessionStore
}

type codexWebsocketSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*codexWebsocketSession
}

var globalCodexWebsocketSessionStore = &codexWebsocketSessionStore{
	sessions: make(map[string]*codexWebsocketSession),
}

var errCodexWebsocketStaleTerminalClose = errors.New("codex websockets executor: upstream closed after previous terminal event")
var errCodexWebsocketRequestWithoutUpstreamContext = errors.New("codex websockets executor: request requires existing websocket session")

type codexWebsocketSession struct {
	sessionID string

	reqMu sync.Mutex

	connMu                sync.Mutex
	conn                  *websocket.Conn
	wsURL                 string
	authID                string
	suppressReadErrorConn *websocket.Conn
	terminalStateConn     *websocket.Conn
	upstreamStateLost     bool

	writeMu sync.Mutex

	activeMu     sync.Mutex
	activeCh     chan codexWebsocketRead
	activeDone   <-chan struct{}
	activeCancel context.CancelFunc
	activeAccept bool

	readerConn *websocket.Conn

	upstreamDisconnectOnce sync.Once
	upstreamDisconnectCh   chan error
}

func NewCodexWebsocketsExecutor(cfg *config.Config) *CodexWebsocketsExecutor {
	return &CodexWebsocketsExecutor{
		CodexExecutor: NewCodexExecutor(cfg),
		store:         globalCodexWebsocketSessionStore,
	}
}

type codexWebsocketRead struct {
	conn    *websocket.Conn
	msgType int
	payload []byte
	err     error
}

func (s *codexWebsocketSession) setActive(ch chan codexWebsocketRead) {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	if s.activeCancel != nil {
		s.activeCancel()
		s.activeCancel = nil
		s.activeDone = nil
	}
	s.activeCh = ch
	s.activeAccept = ch != nil
	if ch != nil {
		activeCtx, activeCancel := context.WithCancel(context.Background())
		s.activeDone = activeCtx.Done()
		s.activeCancel = activeCancel
	}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) clearActive(ch chan codexWebsocketRead) {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	if s.activeCh == ch {
		s.activeCh = nil
		if s.activeCancel != nil {
			s.activeCancel()
		}
		s.activeAccept = false
		s.activeCancel = nil
		s.activeDone = nil
	}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) hasUpstreamConn() bool {
	if s == nil {
		return false
	}
	s.connMu.Lock()
	conn := s.conn
	s.connMu.Unlock()
	return conn != nil
}

func (s *codexWebsocketSession) markTerminalStateConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.connMu.Lock()
	if s.conn == conn {
		s.terminalStateConn = conn
	}
	s.connMu.Unlock()
}

func (s *codexWebsocketSession) markLostTerminalStateForConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.connMu.Lock()
	if s.terminalStateConn == conn {
		s.upstreamStateLost = true
	}
	s.connMu.Unlock()
}

func (s *codexWebsocketSession) clearLostTerminalState() {
	if s == nil {
		return
	}
	s.connMu.Lock()
	s.upstreamStateLost = false
	s.terminalStateConn = nil
	s.connMu.Unlock()
}

func (s *codexWebsocketSession) lostTerminalState() bool {
	if s == nil {
		return false
	}
	s.connMu.Lock()
	lost := s.upstreamStateLost
	s.connMu.Unlock()
	return lost
}

func (s *codexWebsocketSession) connHasTerminalState(conn *websocket.Conn) bool {
	if s == nil || conn == nil {
		return false
	}
	s.connMu.Lock()
	hasTerminalState := s.terminalStateConn == conn
	s.connMu.Unlock()
	return hasTerminalState
}

func (s *codexWebsocketSession) stopActiveDelivery(ch chan codexWebsocketRead) {
	if s == nil {
		return
	}
	s.activeMu.Lock()
	if s.activeCh == ch {
		s.activeAccept = false
	}
	s.activeMu.Unlock()
}

func (s *codexWebsocketSession) activeChannelUnchangedOrCleared(ch chan codexWebsocketRead) bool {
	if s == nil {
		return false
	}
	s.activeMu.Lock()
	activeCh := s.activeCh
	s.activeMu.Unlock()
	return activeCh == nil || activeCh == ch
}

func (s *codexWebsocketSession) deliverActiveRead(ev codexWebsocketRead) bool {
	if s == nil {
		return false
	}
	s.activeMu.Lock()
	ch := s.activeCh
	done := s.activeDone
	accept := s.activeAccept
	s.activeMu.Unlock()
	if ch == nil || !accept {
		return false
	}
	delivered := false
	select {
	case ch <- ev:
		delivered = true
	case <-done:
	default:
	}
	s.clearActive(ch)
	close(ch)
	return delivered
}

func (s *codexWebsocketSession) writeMessage(conn *websocket.Conn, msgType int, payload []byte) error {
	if s == nil {
		return fmt.Errorf("codex websockets executor: session is nil")
	}
	if conn == nil {
		return fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteMessage(msgType, payload)
}

func (s *codexWebsocketSession) suppressReadErrorForConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.connMu.Lock()
	if s.conn == conn {
		s.suppressReadErrorConn = conn
	}
	s.connMu.Unlock()
}

func (s *codexWebsocketSession) shouldSuppressReadErrorForConn(conn *websocket.Conn) bool {
	if s == nil || conn == nil {
		return false
	}
	s.connMu.Lock()
	current := s.conn
	suppressed := s.suppressReadErrorConn == conn
	s.connMu.Unlock()
	return current != conn || suppressed
}

func (s *codexWebsocketSession) configureConn(conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	conn.SetPingHandler(func(appData string) error {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		// Reply pongs from the same write lock to avoid concurrent writes.
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})
}

func (s *codexWebsocketSession) notifyUpstreamDisconnect(err error) {
	if s == nil {
		return
	}
	s.upstreamDisconnectOnce.Do(func() {
		if s.upstreamDisconnectCh == nil {
			return
		}
		select {
		case s.upstreamDisconnectCh <- err:
		default:
		}
		close(s.upstreamDisconnectCh)
	})
}

func (e *CodexWebsocketsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Alt == "responses/compact" {
		return e.CodexExecutor.executeCompact(ctx, auth, req, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex websockets executor", body)

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return resp, err
	}

	body, wsHeaders, errPromptCache := applyCodexPromptCacheHeadersWithContext(ctx, from, req, body)
	if errPromptCache != nil {
		return resp, errPromptCache
	}
	clientBody := body
	var identityState codexIdentityConfuseState
	upstreamBody, identityState := applyCodexIdentityConfuseBody(e.cfg, auth, originalPayloadSource, body)
	reporter.SetTranslatedReasoningEffort(clientBody, to.String())
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg)
	applyCodexIdentityConfuseHeaders(wsHeaders, &identityState)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	executionSessionID := executionSessionIDFromOptions(opts)
	var sess *codexWebsocketSession
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		sess.reqMu.Lock()
		defer sess.reqMu.Unlock()
	}
	if sess != nil && codexWebsocketRequestStartsFreshContext(upstreamBody) {
		sess.clearLostTerminalState()
	}
	if sess != nil && codexWebsocketRequestRequiresExistingUpstream(sess, upstreamBody) && !sess.hasUpstreamConn() {
		errAppend := e.failCodexWebsocketRequestWithoutUpstreamContext(ctx, sess, nil, "request_without_upstream_context", nil)
		return resp, errAppend
	}

	wsReqBody := buildCodexWebsocketRequestBody(upstreamBody)
	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      wsReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	conn, respHS, errDial := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
	if errDial != nil {
		bodyErr := websocketHandshakeBody(respHS)
		if respHS != nil {
			helps.RecordAPIWebsocketUpgradeRejection(ctx, e.cfg, websocketUpgradeRequestLog(wsReqLog), respHS.StatusCode, respHS.Header.Clone(), bodyErr)
		}
		if respHS != nil && respHS.StatusCode == http.StatusUpgradeRequired {
			return e.CodexExecutor.Execute(ctx, auth, req, opts)
		}
		if respHS != nil && respHS.StatusCode > 0 {
			return resp, statusErr{code: respHS.StatusCode, msg: string(bodyErr)}
		}
		helps.RecordAPIWebsocketError(ctx, e.cfg, "dial", errDial)
		return resp, errDial
	}
	if sess != nil && respHS != nil && codexWebsocketRequestRequiresExistingUpstream(sess, upstreamBody) {
		errAppend := e.failCodexWebsocketRequestWithoutUpstreamContext(ctx, sess, conn, "request_without_upstream_context", nil)
		return resp, errAppend
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	reporter.StartResponseTTFT()
	if sess == nil {
		logCodexWebsocketConnected(executionSessionID, authID, wsURL)
		defer func() {
			reason := "completed"
			if err != nil {
				reason = "error"
			}
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, reason, err)
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		}()
	}

	var readCh chan codexWebsocketRead
	if sess != nil {
		readCh = make(chan codexWebsocketRead, 4096)
		sess.setActive(readCh)
		defer func() {
			sess.clearActive(readCh)
		}()
	}

	if errSend := writeCodexWebsocketMessage(sess, conn, wsReqBody); errSend != nil {
		if sess != nil {
			if codexWebsocketRequestCannotRetryFresh(sess, conn, upstreamBody) {
				errAppend := e.failCodexWebsocketRequestWithoutUpstreamContext(ctx, sess, conn, "send_append_without_retry", errSend)
				return resp, errAppend
			}
			sess.suppressReadErrorForConn(conn)
			sess.stopActiveDelivery(readCh)
			if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, conn, errSend, upstreamBody) {
				e.clearUpstreamConn(sess, conn, "send_close_sent_http_fallback", errSend, false)
				helps.RecordAPIWebsocketError(ctx, e.cfg, "send_close_sent_http_fallback", errSend)
				fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
				return e.CodexExecutor.Execute(ctx, auth, fallbackReq, fallbackOpts)
			}
			e.clearUpstreamConn(sess, conn, "send_error", errSend, false)
			readCh = make(chan codexWebsocketRead, 4096)
			sess.setActive(readCh)

			// Retry with fresh websocket connections. This mainly handles upstream
			// closing sockets between sequential requests within the same execution
			// session, including close frames that race with the first resend.
			for sendRetryAttempt := 0; ; sendRetryAttempt++ {
				connRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
				if errDialRetry != nil || connRetry == nil {
					closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
					if errDialRetry == nil {
						errDialRetry = fmt.Errorf("codex websockets executor: websocket retry returned nil conn")
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry", errDialRetry)
					sess.notifyUpstreamDisconnect(errDialRetry)
					return resp, errDialRetry
				}
				wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
				helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
					URL:       wsURL,
					Method:    "WEBSOCKET",
					Headers:   wsHeaders.Clone(),
					Body:      wsReqBodyRetry,
					Provider:  e.Identifier(),
					AuthID:    authID,
					AuthLabel: authLabel,
					AuthType:  authType,
					AuthValue: authValue,
				})
				recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
				reporter.StartResponseTTFT()
				if errSendRetry := writeCodexWebsocketMessage(sess, connRetry, wsReqBodyRetry); errSendRetry == nil {
					conn = connRetry
					wsReqBody = wsReqBodyRetry
					break
				} else {
					if shouldRetryCodexWebsocketSendError(sess, connRetry, errSendRetry, upstreamBody, sendRetryAttempt) && !codexWebsocketRequestCannotRetryFresh(sess, connRetry, upstreamBody) {
						sess.suppressReadErrorForConn(connRetry)
						sess.stopActiveDelivery(readCh)
						e.clearUpstreamConn(sess, connRetry, "send_retry_close_sent", errSendRetry, false)
						readCh = make(chan codexWebsocketRead, 4096)
						sess.setActive(readCh)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry_close_sent", errSendRetry)
						continue
					}
					if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, connRetry, errSendRetry, upstreamBody) {
						e.clearUpstreamConn(sess, connRetry, "send_close_sent_http_fallback", errSendRetry, false)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_close_sent_http_fallback", errSendRetry)
						fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
						return e.CodexExecutor.Execute(ctx, auth, fallbackReq, fallbackOpts)
					}
					e.invalidateUpstreamConn(sess, connRetry, "send_error", errSendRetry)
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry", errSendRetry)
					return resp, errSendRetry
				}
			}
		} else {
			helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
			return resp, errSend
		}
	}

	readRetryAttempt := 0
	receivedPayload := false
	for {
		if ctx != nil && ctx.Err() != nil {
			return resp, ctx.Err()
		}
		msgType, payload, errRead := readCodexWebsocketMessage(ctx, sess, conn, readCh)
		if errRead != nil {
			if sess != nil && isCodexWebsocketStaleTerminalCloseError(errRead) {
				if !canRetryCodexWebsocketRequestAfterStaleTerminalClose(sess, upstreamBody) {
					helps.RecordAPIWebsocketError(ctx, e.cfg, "read_stale_terminal_close_append_without_retry", errRead)
					sess.notifyUpstreamDisconnect(errRead)
					return resp, errRead
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read_stale_terminal_close_retry", errRead)
				readCh = make(chan codexWebsocketRead, 4096)
				sess.setActive(readCh)
				connRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
				if errDialRetry != nil || connRetry == nil {
					closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
					if errDialRetry == nil {
						errDialRetry = fmt.Errorf("codex websockets executor: websocket retry returned nil conn")
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry_stale_terminal_close", errDialRetry)
					sess.notifyUpstreamDisconnect(errDialRetry)
					return resp, errDialRetry
				}
				wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
				helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
					URL:       wsURL,
					Method:    "WEBSOCKET",
					Headers:   wsHeaders.Clone(),
					Body:      wsReqBodyRetry,
					Provider:  e.Identifier(),
					AuthID:    authID,
					AuthLabel: authLabel,
					AuthType:  authType,
					AuthValue: authValue,
				})
				recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
				reporter.StartResponseTTFT()
				if errSendRetry := writeCodexWebsocketMessage(sess, connRetry, wsReqBodyRetry); errSendRetry != nil {
					e.invalidateUpstreamConn(sess, connRetry, "send_error", errSendRetry)
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry_stale_terminal_close", errSendRetry)
					return resp, errSendRetry
				}
				conn = connRetry
				wsReqBody = wsReqBodyRetry
				continue
			}
			if sess != nil && !receivedPayload && shouldFallbackCodexWebsocketPrePayloadReadErrorToHTTP(sess, conn, errRead, upstreamBody) {
				e.clearUpstreamConn(sess, conn, "read_pre_payload_close_http_fallback", errRead, false)
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read_pre_payload_close_http_fallback", errRead)
				fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
				return e.CodexExecutor.Execute(ctx, auth, fallbackReq, fallbackOpts)
			}
			if sess != nil && !receivedPayload && shouldRetryCodexWebsocketPrePayloadReadError(errRead, readRetryAttempt) && !codexWebsocketRequestCannotRetryFresh(sess, conn, upstreamBody) {
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read_pre_payload_close_retry", errRead)
				readRetryAttempt++
				e.clearUpstreamConn(sess, conn, "pre_payload_close_retry", errRead, false)
				readCh = make(chan codexWebsocketRead, 4096)
				sess.setActive(readCh)
				connRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
				if errDialRetry != nil || connRetry == nil {
					closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
					if errDialRetry == nil {
						errDialRetry = fmt.Errorf("codex websockets executor: websocket retry returned nil conn")
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry_pre_payload_close", errDialRetry)
					sess.notifyUpstreamDisconnect(errDialRetry)
					return resp, errDialRetry
				}
				wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
				helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
					URL:       wsURL,
					Method:    "WEBSOCKET",
					Headers:   wsHeaders.Clone(),
					Body:      wsReqBodyRetry,
					Provider:  e.Identifier(),
					AuthID:    authID,
					AuthLabel: authLabel,
					AuthType:  authType,
					AuthValue: authValue,
				})
				recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
				reporter.StartResponseTTFT()
				if errSendRetry := writeCodexWebsocketMessage(sess, connRetry, wsReqBodyRetry); errSendRetry != nil {
					if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, connRetry, errSendRetry, upstreamBody) {
						e.clearUpstreamConn(sess, connRetry, "send_pre_payload_close_http_fallback", errSendRetry, false)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_pre_payload_close_http_fallback", errSendRetry)
						fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
						return e.CodexExecutor.Execute(ctx, auth, fallbackReq, fallbackOpts)
					}
					e.invalidateUpstreamConn(sess, connRetry, "send_error", errSendRetry)
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry_pre_payload_close", errSendRetry)
					return resp, errSendRetry
				}
				conn = connRetry
				wsReqBody = wsReqBodyRetry
				continue
			}
			if !receivedPayload && isCodexWebsocketMessageTooBigError(errRead) && canFallbackCodexWebsocketRequestToHTTP(sess, conn, upstreamBody) {
				if sess != nil {
					e.clearUpstreamConn(sess, conn, "message_too_big_http_fallback", errRead, false)
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read_message_too_big_http_fallback", errRead)
				fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
				return e.CodexExecutor.Execute(ctx, auth, fallbackReq, fallbackOpts)
			}
			if sess != nil {
				e.invalidateUpstreamConn(sess, conn, "read_error", errRead)
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "read", errRead)
			return resp, errRead
		}
		if msgType != websocket.TextMessage {
			if msgType == websocket.BinaryMessage {
				err = fmt.Errorf("codex websockets executor: unexpected binary message")
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "unexpected_binary", err)
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", err)
				return resp, err
			}
			continue
		}

		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 {
			continue
		}
		receivedPayload = true
		reporter.MarkFirstResponseByte()
		payload = applyCodexIdentityConfuseResponsePayload(payload, identityState)
		helps.AppendAPIWebsocketResponse(ctx, e.cfg, payload)

		if wsErr, ok := parseCodexWebsocketError(payload); ok {
			if sess != nil {
				e.invalidateUpstreamConn(sess, conn, "upstream_error", wsErr)
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
			return resp, wsErr
		}
		if wsErr, ok := codexWebsocketStatuslessErrorEvent(payload); ok {
			if sess != nil {
				e.invalidateUpstreamConn(sess, conn, "upstream_error", wsErr)
			}
			helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
			return resp, wsErr
		}

		payload = normalizeCodexWebsocketCompletion(payload)
		eventType := gjson.GetBytes(payload, "type").String()
		if eventType == "response.completed" {
			if detail, ok := helps.ParseCodexUsage(payload); ok {
				reporter.Publish(ctx, detail)
			}
			var param any
			clientPayload := applyCodexIdentityExposeResponsePayload(payload, identityState)
			out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, clientBody, clientPayload, &param)
			resp = cliproxyexecutor.Response{Payload: out}
			return resp, nil
		}
	}
}

func (e *CodexWebsocketsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	log.Debugf("Executing Codex Websockets stream request with auth ID: %s, model: %s", auth.ID, req.Model)
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	body := req.Payload
	userPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		userPayload = opts.OriginalRequest
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, body, requestedModel, requestPath, opts.Headers)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex websockets executor", body)

	httpURL := strings.TrimSuffix(baseURL, "/") + "/responses"
	wsURL, err := buildCodexResponsesWebsocketURL(httpURL)
	if err != nil {
		return nil, err
	}

	body, wsHeaders, errPromptCache := applyCodexPromptCacheHeadersWithContext(ctx, from, req, body)
	if errPromptCache != nil {
		return nil, errPromptCache
	}
	clientBody := body
	var identityState codexIdentityConfuseState
	upstreamBody, identityState := applyCodexIdentityConfuseBody(e.cfg, auth, userPayload, body)
	reporter.SetTranslatedReasoningEffort(clientBody, to.String())
	wsHeaders = applyCodexWebsocketHeaders(ctx, wsHeaders, auth, apiKey, e.cfg)
	applyCodexIdentityConfuseHeaders(wsHeaders, &identityState)

	var authID, authLabel, authType, authValue string
	authID = auth.ID
	authLabel = auth.Label
	authType, authValue = auth.AccountInfo()

	executionSessionID := executionSessionIDFromOptions(opts)
	var sess *codexWebsocketSession
	if executionSessionID != "" {
		sess = e.getOrCreateSession(executionSessionID)
		if sess != nil {
			sess.reqMu.Lock()
		}
	}
	if sess != nil && codexWebsocketRequestStartsFreshContext(upstreamBody) {
		sess.clearLostTerminalState()
	}
	if sess != nil && codexWebsocketRequestRequiresExistingUpstream(sess, upstreamBody) && !sess.hasUpstreamConn() {
		errAppend := e.failCodexWebsocketRequestWithoutUpstreamContext(ctx, sess, nil, "request_without_upstream_context", nil)
		sess.reqMu.Unlock()
		return nil, errAppend
	}

	wsReqBody := buildCodexWebsocketRequestBody(upstreamBody)
	wsReqLog := helps.UpstreamRequestLog{
		URL:       wsURL,
		Method:    "WEBSOCKET",
		Headers:   wsHeaders.Clone(),
		Body:      wsReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	}
	helps.RecordAPIWebsocketRequest(ctx, e.cfg, wsReqLog)

	conn, respHS, errDial := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
	var upstreamHeaders http.Header
	if respHS != nil {
		upstreamHeaders = respHS.Header.Clone()
	}
	if errDial != nil {
		bodyErr := websocketHandshakeBody(respHS)
		if respHS != nil {
			helps.RecordAPIWebsocketUpgradeRejection(ctx, e.cfg, websocketUpgradeRequestLog(wsReqLog), respHS.StatusCode, respHS.Header.Clone(), bodyErr)
		}
		if respHS != nil && respHS.StatusCode == http.StatusUpgradeRequired {
			return e.CodexExecutor.ExecuteStream(ctx, auth, req, opts)
		}
		if respHS != nil && respHS.StatusCode > 0 {
			return nil, statusErr{code: respHS.StatusCode, msg: string(bodyErr)}
		}
		helps.RecordAPIWebsocketError(ctx, e.cfg, "dial", errDial)
		if sess != nil {
			sess.reqMu.Unlock()
		}
		return nil, errDial
	}
	if sess != nil && respHS != nil && codexWebsocketRequestRequiresExistingUpstream(sess, upstreamBody) {
		errAppend := e.failCodexWebsocketRequestWithoutUpstreamContext(ctx, sess, conn, "request_without_upstream_context", nil)
		sess.reqMu.Unlock()
		return nil, errAppend
	}
	recordAPIWebsocketHandshake(ctx, e.cfg, respHS)
	reporter.StartResponseTTFT()

	if sess == nil {
		logCodexWebsocketConnected(executionSessionID, authID, wsURL)
	}

	var readCh chan codexWebsocketRead
	if sess != nil {
		readCh = make(chan codexWebsocketRead, 4096)
		sess.setActive(readCh)
	}

	if errSend := writeCodexWebsocketMessage(sess, conn, wsReqBody); errSend != nil {
		helps.RecordAPIWebsocketError(ctx, e.cfg, "send", errSend)
		if sess != nil {
			if codexWebsocketRequestCannotRetryFresh(sess, conn, upstreamBody) {
				errAppend := e.failCodexWebsocketRequestWithoutUpstreamContext(ctx, sess, conn, "send_append_without_retry", errSend)
				sess.clearActive(readCh)
				sess.reqMu.Unlock()
				return nil, errAppend
			}
			sess.suppressReadErrorForConn(conn)
			sess.stopActiveDelivery(readCh)
			if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, conn, errSend, upstreamBody) {
				e.clearUpstreamConn(sess, conn, "send_close_sent_http_fallback", errSend, false)
				helps.RecordAPIWebsocketError(ctx, e.cfg, "send_close_sent_http_fallback", errSend)
				sess.clearActive(readCh)
				fallbackResult, errFallback := e.startCodexHTTPFallbackStream(ctx, auth, req, opts, reporter, func() {
					sess.reqMu.Unlock()
				})
				if errFallback != nil {
					sess.reqMu.Unlock()
					return nil, errFallback
				}
				return fallbackResult, nil
			}
			e.clearUpstreamConn(sess, conn, "send_error", errSend, false)
			readCh = make(chan codexWebsocketRead, 4096)
			sess.setActive(readCh)

			// Retry with fresh websocket connections for the same execution session.
			for sendRetryAttempt := 0; ; sendRetryAttempt++ {
				connRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
				if errDialRetry != nil || connRetry == nil {
					closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
					if errDialRetry == nil {
						errDialRetry = fmt.Errorf("codex websockets executor: websocket retry returned nil conn")
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry", errDialRetry)
					sess.notifyUpstreamDisconnect(errDialRetry)
					sess.clearActive(readCh)
					sess.reqMu.Unlock()
					return nil, errDialRetry
				}
				wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
				helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
					URL:       wsURL,
					Method:    "WEBSOCKET",
					Headers:   wsHeaders.Clone(),
					Body:      wsReqBodyRetry,
					Provider:  e.Identifier(),
					AuthID:    authID,
					AuthLabel: authLabel,
					AuthType:  authType,
					AuthValue: authValue,
				})
				recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
				reporter.StartResponseTTFT()
				if errSendRetry := writeCodexWebsocketMessage(sess, connRetry, wsReqBodyRetry); errSendRetry != nil {
					if shouldRetryCodexWebsocketSendError(sess, connRetry, errSendRetry, upstreamBody, sendRetryAttempt) && !codexWebsocketRequestCannotRetryFresh(sess, connRetry, upstreamBody) {
						sess.suppressReadErrorForConn(connRetry)
						sess.stopActiveDelivery(readCh)
						e.clearUpstreamConn(sess, connRetry, "send_retry_close_sent", errSendRetry, false)
						readCh = make(chan codexWebsocketRead, 4096)
						sess.setActive(readCh)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry_close_sent", errSendRetry)
						continue
					}
					if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, connRetry, errSendRetry, upstreamBody) {
						e.clearUpstreamConn(sess, connRetry, "send_close_sent_http_fallback", errSendRetry, false)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_close_sent_http_fallback", errSendRetry)
						sess.clearActive(readCh)
						fallbackResult, errFallback := e.startCodexHTTPFallbackStream(ctx, auth, req, opts, reporter, func() {
							sess.reqMu.Unlock()
						})
						if errFallback != nil {
							sess.reqMu.Unlock()
							return nil, errFallback
						}
						return fallbackResult, nil
					}
					helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry", errSendRetry)
					e.invalidateUpstreamConn(sess, connRetry, "send_error", errSendRetry)
					sess.clearActive(readCh)
					sess.reqMu.Unlock()
					return nil, errSendRetry
				}
				conn = connRetry
				wsReqBody = wsReqBodyRetry
				break
			}
		} else {
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, "send_error", errSend)
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
			return nil, errSend
		}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		terminateReason := "completed"
		var terminateErr error

		defer close(out)
		defer func() {
			if sess != nil {
				sess.clearActive(readCh)
				sess.reqMu.Unlock()
				return
			}
			logCodexWebsocketDisconnected(executionSessionID, authID, wsURL, terminateReason, terminateErr)
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("codex websockets executor: close websocket error: %v", errClose)
			}
		}()

		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		var param any
		readRetryAttempt := 0
		sentPayload := false
		for {
			if ctx != nil && ctx.Err() != nil {
				terminateReason = "context_done"
				terminateErr = ctx.Err()
				_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
				return
			}
			msgType, payload, errRead := readCodexWebsocketMessage(ctx, sess, conn, readCh)
			if errRead != nil {
				if sess != nil && ctx != nil && ctx.Err() != nil {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					_ = send(cliproxyexecutor.StreamChunk{Err: ctx.Err()})
					return
				}
				terminateReason = "read_error"
				terminateErr = errRead
				if sess != nil && !sentPayload && isCodexWebsocketStaleTerminalCloseError(errRead) {
					if !canRetryCodexWebsocketRequestAfterStaleTerminalClose(sess, upstreamBody) {
						terminateReason = "stale_terminal_close_append_without_retry"
						helps.RecordAPIWebsocketError(ctx, e.cfg, "read_stale_terminal_close_append_without_retry", errRead)
						sess.notifyUpstreamDisconnect(errRead)
						reporter.PublishFailure(ctx, errRead)
						_ = send(cliproxyexecutor.StreamChunk{Err: errRead})
						return
					}
					terminateReason = "stale_terminal_close_retry"
					helps.RecordAPIWebsocketError(ctx, e.cfg, "read_stale_terminal_close_retry", errRead)
					readCh = make(chan codexWebsocketRead, 4096)
					sess.setActive(readCh)
					connRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
					if errDialRetry != nil || connRetry == nil {
						closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
						if errDialRetry == nil {
							errDialRetry = fmt.Errorf("codex websockets executor: websocket retry returned nil conn")
						}
						terminateErr = errDialRetry
						helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry_stale_terminal_close", errDialRetry)
						sess.notifyUpstreamDisconnect(errDialRetry)
						reporter.PublishFailure(ctx, errDialRetry)
						_ = send(cliproxyexecutor.StreamChunk{Err: errDialRetry})
						return
					}
					wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
					helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
						URL:       wsURL,
						Method:    "WEBSOCKET",
						Headers:   wsHeaders.Clone(),
						Body:      wsReqBodyRetry,
						Provider:  e.Identifier(),
						AuthID:    authID,
						AuthLabel: authLabel,
						AuthType:  authType,
						AuthValue: authValue,
					})
					recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
					reporter.StartResponseTTFT()
					if errSendRetry := writeCodexWebsocketMessage(sess, connRetry, wsReqBodyRetry); errSendRetry != nil {
						terminateReason = "send_retry_stale_terminal_close"
						terminateErr = errSendRetry
						if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, connRetry, errSendRetry, upstreamBody) {
							terminateReason = "send_stale_terminal_close_http_fallback"
							helps.RecordAPIWebsocketError(ctx, e.cfg, "send_stale_terminal_close_http_fallback", errSendRetry)
							e.clearUpstreamConn(sess, connRetry, terminateReason, errSendRetry, false)
							fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
							fallbackResult, errFallback := e.CodexExecutor.ExecuteStream(ctx, auth, fallbackReq, fallbackOpts)
							if errFallback != nil {
								terminateErr = errFallback
								reporter.PublishFailure(ctx, errFallback)
								_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
								return
							}
							if fallbackResult == nil {
								errFallback = fmt.Errorf("codex websockets executor: HTTP fallback returned nil stream")
								terminateErr = errFallback
								reporter.PublishFailure(ctx, errFallback)
								_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
								return
							}
							okFallback, errFallbackStream := forwardCodexHTTPFallbackStream(ctx, fallbackResult.Chunks, send, &sentPayload)
							if errFallbackStream != nil {
								terminateErr = errFallbackStream
								reporter.PublishFailure(ctx, errFallbackStream)
							}
							if !okFallback {
								terminateReason = "context_done"
								terminateErr = ctx.Err()
							}
							return
						}
						e.invalidateUpstreamConn(sess, connRetry, "send_error", errSendRetry)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry_stale_terminal_close", errSendRetry)
						reporter.PublishFailure(ctx, errSendRetry)
						_ = send(cliproxyexecutor.StreamChunk{Err: errSendRetry})
						return
					}
					conn = connRetry
					wsReqBody = wsReqBodyRetry
					terminateReason = "completed"
					terminateErr = nil
					continue
				}
				if sess != nil && !sentPayload && shouldFallbackCodexWebsocketPrePayloadReadErrorToHTTP(sess, conn, errRead, upstreamBody) {
					terminateReason = "read_pre_payload_close_http_fallback"
					terminateErr = errRead
					helps.RecordAPIWebsocketError(ctx, e.cfg, "read_pre_payload_close_http_fallback", errRead)
					e.clearUpstreamConn(sess, conn, terminateReason, errRead, false)
					fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
					fallbackResult, errFallback := e.CodexExecutor.ExecuteStream(ctx, auth, fallbackReq, fallbackOpts)
					if errFallback != nil {
						terminateErr = errFallback
						reporter.PublishFailure(ctx, errFallback)
						_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
						return
					}
					if fallbackResult == nil {
						errFallback = fmt.Errorf("codex websockets executor: HTTP fallback returned nil stream")
						terminateErr = errFallback
						reporter.PublishFailure(ctx, errFallback)
						_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
						return
					}
					okFallback, errFallbackStream := forwardCodexHTTPFallbackStream(ctx, fallbackResult.Chunks, send, &sentPayload)
					if errFallbackStream != nil {
						terminateErr = errFallbackStream
						reporter.PublishFailure(ctx, errFallbackStream)
					}
					if !okFallback {
						terminateReason = "context_done"
						terminateErr = ctx.Err()
					}
					return
				}
				if sess != nil && !sentPayload && shouldRetryCodexWebsocketPrePayloadReadError(errRead, readRetryAttempt) && !codexWebsocketRequestCannotRetryFresh(sess, conn, upstreamBody) {
					terminateReason = "pre_payload_close_retry"
					terminateErr = errRead
					helps.RecordAPIWebsocketError(ctx, e.cfg, "read_pre_payload_close_retry", errRead)
					readRetryAttempt++
					e.clearUpstreamConn(sess, conn, terminateReason, errRead, false)
					readCh = make(chan codexWebsocketRead, 4096)
					sess.setActive(readCh)
					connRetry, respHSRetry, errDialRetry := e.ensureUpstreamConn(ctx, auth, sess, authID, wsURL, wsHeaders)
					if errDialRetry != nil || connRetry == nil {
						closeHTTPResponseBody(respHSRetry, "codex websockets executor: close handshake response body error")
						if errDialRetry == nil {
							errDialRetry = fmt.Errorf("codex websockets executor: websocket retry returned nil conn")
						}
						terminateErr = errDialRetry
						helps.RecordAPIWebsocketError(ctx, e.cfg, "dial_retry_pre_payload_close", errDialRetry)
						sess.notifyUpstreamDisconnect(errDialRetry)
						reporter.PublishFailure(ctx, errDialRetry)
						_ = send(cliproxyexecutor.StreamChunk{Err: errDialRetry})
						return
					}
					wsReqBodyRetry := buildCodexWebsocketRequestBody(upstreamBody)
					helps.RecordAPIWebsocketRequest(ctx, e.cfg, helps.UpstreamRequestLog{
						URL:       wsURL,
						Method:    "WEBSOCKET",
						Headers:   wsHeaders.Clone(),
						Body:      wsReqBodyRetry,
						Provider:  e.Identifier(),
						AuthID:    authID,
						AuthLabel: authLabel,
						AuthType:  authType,
						AuthValue: authValue,
					})
					recordAPIWebsocketHandshake(ctx, e.cfg, respHSRetry)
					reporter.StartResponseTTFT()
					if errSendRetry := writeCodexWebsocketMessage(sess, connRetry, wsReqBodyRetry); errSendRetry != nil {
						terminateReason = "send_retry_pre_payload_close"
						terminateErr = errSendRetry
						if shouldFallbackCodexWebsocketSendErrorToHTTP(sess, connRetry, errSendRetry, upstreamBody) {
							terminateReason = "send_pre_payload_close_http_fallback"
							helps.RecordAPIWebsocketError(ctx, e.cfg, "send_pre_payload_close_http_fallback", errSendRetry)
							e.clearUpstreamConn(sess, connRetry, terminateReason, errSendRetry, false)
							fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
							fallbackResult, errFallback := e.CodexExecutor.ExecuteStream(ctx, auth, fallbackReq, fallbackOpts)
							if errFallback != nil {
								terminateErr = errFallback
								reporter.PublishFailure(ctx, errFallback)
								_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
								return
							}
							if fallbackResult == nil {
								errFallback = fmt.Errorf("codex websockets executor: HTTP fallback returned nil stream")
								terminateErr = errFallback
								reporter.PublishFailure(ctx, errFallback)
								_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
								return
							}
							okFallback, errFallbackStream := forwardCodexHTTPFallbackStream(ctx, fallbackResult.Chunks, send, &sentPayload)
							if errFallbackStream != nil {
								terminateErr = errFallbackStream
								reporter.PublishFailure(ctx, errFallbackStream)
							}
							if !okFallback {
								terminateReason = "context_done"
								terminateErr = ctx.Err()
							}
							return
						}
						e.invalidateUpstreamConn(sess, connRetry, "send_error", errSendRetry)
						helps.RecordAPIWebsocketError(ctx, e.cfg, "send_retry_pre_payload_close", errSendRetry)
						reporter.PublishFailure(ctx, errSendRetry)
						_ = send(cliproxyexecutor.StreamChunk{Err: errSendRetry})
						return
					}
					conn = connRetry
					wsReqBody = wsReqBodyRetry
					terminateReason = "completed"
					terminateErr = nil
					continue
				}
				if !sentPayload && isCodexWebsocketMessageTooBigError(errRead) && canFallbackCodexWebsocketRequestToHTTP(sess, conn, upstreamBody) {
					terminateReason = "message_too_big_http_fallback"
					helps.RecordAPIWebsocketError(ctx, e.cfg, "read_message_too_big_http_fallback", errRead)
					if sess != nil {
						e.clearUpstreamConn(sess, conn, terminateReason, errRead, false)
					}
					fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
					fallbackResult, errFallback := e.CodexExecutor.ExecuteStream(ctx, auth, fallbackReq, fallbackOpts)
					if errFallback != nil {
						terminateErr = errFallback
						reporter.PublishFailure(ctx, errFallback)
						_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
						return
					}
					if fallbackResult == nil {
						errFallback = fmt.Errorf("codex websockets executor: HTTP fallback returned nil stream")
						terminateErr = errFallback
						reporter.PublishFailure(ctx, errFallback)
						_ = send(cliproxyexecutor.StreamChunk{Err: errFallback})
						return
					}
					okFallback, errFallbackStream := forwardCodexHTTPFallbackStream(ctx, fallbackResult.Chunks, send, &sentPayload)
					if errFallbackStream != nil {
						terminateErr = errFallbackStream
						reporter.PublishFailure(ctx, errFallbackStream)
					}
					if !okFallback {
						terminateReason = "context_done"
						terminateErr = ctx.Err()
					}
					return
				}
				helps.RecordAPIWebsocketError(ctx, e.cfg, "read", errRead)
				reporter.PublishFailure(ctx, errRead)
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "read_error", errRead)
				}
				_ = send(cliproxyexecutor.StreamChunk{Err: errRead})
				return
			}
			if msgType != websocket.TextMessage {
				if msgType == websocket.BinaryMessage {
					err = fmt.Errorf("codex websockets executor: unexpected binary message")
					terminateReason = "unexpected_binary"
					terminateErr = err
					helps.RecordAPIWebsocketError(ctx, e.cfg, "unexpected_binary", err)
					reporter.PublishFailure(ctx, err)
					if sess != nil {
						e.invalidateUpstreamConn(sess, conn, "unexpected_binary", err)
					}
					_ = send(cliproxyexecutor.StreamChunk{Err: err})
					return
				}
				continue
			}

			payload = bytes.TrimSpace(payload)
			if len(payload) == 0 {
				continue
			}
			reporter.MarkFirstResponseByte()
			payload = applyCodexIdentityConfuseResponsePayload(payload, identityState)
			helps.AppendAPIWebsocketResponse(ctx, e.cfg, payload)

			if wsErr, ok := parseCodexWebsocketError(payload); ok {
				terminateReason = "upstream_error"
				terminateErr = wsErr
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
				reporter.PublishFailure(ctx, wsErr)
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "upstream_error", wsErr)
				}
				_ = send(cliproxyexecutor.StreamChunk{Err: wsErr})
				return
			}

			eventType := gjson.GetBytes(payload, "type").String()
			isTerminalEvent := eventType == "response.completed" || eventType == "response.done" || eventType == "error"
			clientPayload := applyCodexIdentityExposeResponsePayload(payload, identityState)
			if cliproxyexecutor.DownstreamWebsocket(ctx) {
				if eventType == "response.completed" || eventType == "response.done" {
					if detail, ok := helps.ParseCodexUsage(payload); ok {
						reporter.Publish(ctx, detail)
					}
				}
				if !send(cliproxyexecutor.StreamChunk{Payload: clientPayload}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
				sentPayload = true
				if isTerminalEvent {
					return
				}
				continue
			}

			if wsErr, ok := codexWebsocketStatuslessErrorEvent(payload); ok {
				terminateReason = "upstream_error"
				terminateErr = wsErr
				helps.RecordAPIWebsocketError(ctx, e.cfg, "upstream_error", wsErr)
				reporter.PublishFailure(ctx, wsErr)
				if sess != nil {
					e.invalidateUpstreamConn(sess, conn, "upstream_error", wsErr)
				}
				_ = send(cliproxyexecutor.StreamChunk{Err: wsErr})
				return
			}

			payload = normalizeCodexWebsocketCompletion(payload)
			eventType = gjson.GetBytes(payload, "type").String()
			if eventType == "response.completed" || eventType == "response.done" {
				if detail, ok := helps.ParseCodexUsage(payload); ok {
					reporter.Publish(ctx, detail)
				}
			}

			clientPayload = applyCodexIdentityExposeResponsePayload(payload, identityState)
			line := encodeCodexWebsocketAsSSE(clientPayload)
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, clientBody, clientBody, line, &param)
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					terminateReason = "context_done"
					terminateErr = ctx.Err()
					return
				}
				sentPayload = true
			}
			if eventType == "response.completed" || eventType == "response.done" {
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: upstreamHeaders, Chunks: out}, nil
}

func (e *CodexWebsocketsExecutor) dialCodexWebsocket(ctx context.Context, auth *cliproxyauth.Auth, wsURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	dialer := newProxyAwareWebsocketDialer(e.cfg, auth)
	dialer.HandshakeTimeout = codexResponsesWebsocketHandshakeTO
	dialer.EnableCompression = true
	if ctx == nil {
		ctx = context.Background()
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if conn != nil {
		// Avoid gorilla/websocket flate tail validation issues on some upstreams/Go versions.
		// Negotiating permessage-deflate is fine; we just don't compress outbound messages.
		conn.EnableWriteCompression(false)
	}
	return conn, resp, err
}

func writeCodexWebsocketMessage(sess *codexWebsocketSession, conn *websocket.Conn, payload []byte) error {
	if sess != nil {
		return sess.writeMessage(conn, websocket.TextMessage, payload)
	}
	if conn == nil {
		return fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func buildCodexWebsocketRequestBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}

	// Match codex-rs websocket v2 semantics: every request is `response.create`.
	// Incremental follow-up turns continue on the same websocket using
	// `previous_response_id` + incremental `input`, not `response.append`.
	wsReqBody, errSet := sjson.SetBytes(bytes.Clone(body), "type", "response.create")
	if errSet == nil && len(wsReqBody) > 0 {
		return wsReqBody
	}
	fallback := bytes.Clone(body)
	fallback, _ = sjson.SetBytes(fallback, "type", "response.create")
	return fallback
}

func readCodexWebsocketMessage(ctx context.Context, sess *codexWebsocketSession, conn *websocket.Conn, readCh chan codexWebsocketRead) (int, []byte, error) {
	if sess == nil {
		if conn == nil {
			return 0, nil, fmt.Errorf("codex websockets executor: websocket conn is nil")
		}
		_ = conn.SetReadDeadline(time.Now().Add(codexResponsesWebsocketIdleTimeout))
		msgType, payload, errRead := conn.ReadMessage()
		return msgType, payload, errRead
	}
	if conn == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	if readCh == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: session read channel is nil")
	}
	for {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case ev, ok := <-readCh:
			if !ok {
				return 0, nil, fmt.Errorf("codex websockets executor: session read channel closed")
			}
			if ev.conn != conn {
				continue
			}
			if ev.err != nil {
				return 0, nil, ev.err
			}
			return ev.msgType, ev.payload, nil
		}
	}
}

func isCodexWebsocketMessageTooBigError(err error) bool {
	if err == nil {
		return false
	}
	var closeErr *websocket.CloseError
	return errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig
}

func isCodexWebsocketCloseSentError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, websocket.ErrCloseSent) {
		return true
	}
	return strings.Contains(err.Error(), websocket.ErrCloseSent.Error())
}

func shouldRetryCodexWebsocketSendError(sess *codexWebsocketSession, conn *websocket.Conn, err error, body []byte, retryAttempt int) bool {
	return retryAttempt < codexResponsesWebsocketSendRetryLimit &&
		isCodexWebsocketCloseSentError(err) &&
		!shouldFallbackCodexWebsocketSendErrorToHTTP(sess, conn, err, body)
}

func shouldFallbackCodexWebsocketSendErrorToHTTP(sess *codexWebsocketSession, conn *websocket.Conn, err error, body []byte) bool {
	return isCodexWebsocketCloseSentError(err) && canFallbackCodexWebsocketRequestToHTTP(sess, conn, body)
}

func shouldFallbackCodexWebsocketPrePayloadReadErrorToHTTP(sess *codexWebsocketSession, conn *websocket.Conn, err error, body []byte) bool {
	return shouldRetryCodexWebsocketPrePayloadReadError(err, 0) && canFallbackCodexWebsocketRequestToHTTP(sess, conn, body)
}

func shouldRetryCodexWebsocketPrePayloadReadError(err error, retryAttempt int) bool {
	if retryAttempt >= codexResponsesWebsocketSendRetryLimit || err == nil {
		return false
	}
	if isCodexWebsocketCloseSentError(err) {
		return true
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		return false
	}
	switch closeErr.Code {
	case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNoStatusReceived:
		return true
	default:
		return false
	}
}

func isCodexWebsocketTerminalPayload(payload []byte) bool {
	switch strings.TrimSpace(gjson.GetBytes(payload, "type").String()) {
	case "response.completed", "response.done":
		return true
	default:
		return false
	}
}

func codexWebsocketStaleTerminalCloseError(err error) error {
	if err == nil {
		return errCodexWebsocketStaleTerminalClose
	}
	return fmt.Errorf("%w: %w", errCodexWebsocketStaleTerminalClose, err)
}

func isCodexWebsocketStaleTerminalCloseError(err error) bool {
	return errors.Is(err, errCodexWebsocketStaleTerminalClose)
}

func isCodexWebsocketRequestWithoutUpstreamContextError(err error) bool {
	return errors.Is(err, errCodexWebsocketRequestWithoutUpstreamContext)
}

func codexWebsocketRequestRequiresExistingUpstream(sess *codexWebsocketSession, body []byte) bool {
	if codexWebsocketRequestIsAppendOnly(body) {
		return true
	}
	return sess != nil && sess.lostTerminalState() && codexWebsocketRequestNeedsLiveUpstream(body)
}

func codexWebsocketRequestCannotRetryFresh(sess *codexWebsocketSession, conn *websocket.Conn, body []byte) bool {
	if codexWebsocketRequestIsAppendOnly(body) {
		return true
	}
	return sess != nil && sess.connHasTerminalState(conn) && codexWebsocketRequestNeedsLiveUpstream(body)
}

func codexWebsocketRequestIsAppendOnly(body []byte) bool {
	if strings.TrimSpace(gjson.GetBytes(body, "type").String()) != "response.append" {
		return false
	}
	return !codexWebsocketRequestUsesPreviousResponseID(body)
}

func codexWebsocketRequestUsesPreviousResponseID(body []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != ""
}

func codexWebsocketRequestStartsFreshContext(body []byte) bool {
	return !codexWebsocketRequestNeedsLiveUpstream(body)
}

func codexWebsocketRequestNeedsLiveUpstream(body []byte) bool {
	if codexWebsocketRequestUsesPreviousResponseID(body) || codexWebsocketRequestIsAppendOnly(body) {
		return true
	}
	if strings.TrimSpace(gjson.GetBytes(body, "type").String()) != "response.create" {
		return false
	}
	return !codexWebsocketInputLooksFullTranscript(gjson.GetBytes(body, "input"))
}

func codexWebsocketInputLooksFullTranscript(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "compaction", "compaction_summary", "function_call", "custom_tool_call":
			return true
		case "message":
			if strings.TrimSpace(item.Get("role").String()) == "assistant" {
				return true
			}
		}
	}
	return false
}

func canFallbackCodexWebsocketRequestToHTTP(sess *codexWebsocketSession, conn *websocket.Conn, body []byte) bool {
	if codexWebsocketRequestIsAppendOnly(body) {
		return false
	}
	if sess != nil && sess.connHasTerminalState(conn) && codexWebsocketRequestNeedsLiveUpstream(body) {
		return false
	}
	return strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) == ""
}

func canRetryCodexWebsocketRequestAfterStaleTerminalClose(sess *codexWebsocketSession, body []byte) bool {
	return !codexWebsocketRequestRequiresExistingUpstream(sess, body)
}

func codexHTTPFallbackRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	req.Payload = sanitizeCodexHTTPFallbackPayload(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		opts.OriginalRequest = sanitizeCodexHTTPFallbackPayload(opts.OriginalRequest)
	}
	return req, opts
}

func (e *CodexWebsocketsExecutor) startCodexHTTPFallbackStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, reporter *helps.UsageReporter, afterForward func()) (*cliproxyexecutor.StreamResult, error) {
	fallbackReq, fallbackOpts := codexHTTPFallbackRequest(req, opts)
	fallbackResult, errFallback := e.CodexExecutor.ExecuteStream(ctx, auth, fallbackReq, fallbackOpts)
	if errFallback != nil {
		if reporter != nil {
			reporter.PublishFailure(ctx, errFallback)
		}
		return nil, errFallback
	}
	if fallbackResult == nil {
		errFallback = fmt.Errorf("codex websockets executor: HTTP fallback returned nil stream")
		if reporter != nil {
			reporter.PublishFailure(ctx, errFallback)
		}
		return nil, errFallback
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	send := func(chunk cliproxyexecutor.StreamChunk) bool {
		if ctx == nil {
			out <- chunk
			return true
		}
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		defer close(out)
		if afterForward != nil {
			defer afterForward()
		}
		sentPayload := false
		_, errFallbackStream := forwardCodexHTTPFallbackStream(ctx, fallbackResult.Chunks, send, &sentPayload)
		if errFallbackStream != nil && reporter != nil {
			reporter.PublishFailure(ctx, errFallbackStream)
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: fallbackResult.Headers, Chunks: out}, nil
}

func sanitizeCodexHTTPFallbackPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	out := bytes.Clone(payload)
	if strings.TrimSpace(gjson.GetBytes(out, "type").String()) == "response.create" {
		out, _ = sjson.DeleteBytes(out, "type")
	}
	out, _ = sjson.DeleteBytes(out, "generate")
	out = sanitizeCodexHTTPFallbackInput(out)
	return out
}

func sanitizeCodexHTTPFallbackInput(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	out := payload
	for i, item := range input.Array() {
		if !item.Get("action").Exists() {
			continue
		}
		updated, err := sjson.DeleteBytes(out, fmt.Sprintf("input.%d.action", i))
		if err == nil {
			out = updated
		}
	}
	return out
}

func forwardCodexHTTPFallbackStream(ctx context.Context, chunks <-chan cliproxyexecutor.StreamChunk, send func(cliproxyexecutor.StreamChunk) bool, sentPayload *bool) (bool, error) {
	if chunks == nil {
		return true, nil
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	for {
		select {
		case <-done:
			return false, nil
		case chunk, ok := <-chunks:
			if !ok {
				return true, nil
			}
			if chunk.Err != nil {
				_ = send(cliproxyexecutor.StreamChunk{Err: chunk.Err})
				return true, chunk.Err
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			if !send(cliproxyexecutor.StreamChunk{Payload: chunk.Payload}) {
				return false, nil
			}
			if sentPayload != nil {
				*sentPayload = true
			}
		}
	}
}

func newProxyAwareWebsocketDialer(cfg *config.Config, auth *cliproxyauth.Auth) *websocket.Dialer {
	dialer := &websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  codexResponsesWebsocketHandshakeTO,
		EnableCompression: true,
		NetDialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if proxyURL == "" {
		return dialer
	}

	setting, errParse := proxyutil.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("codex websockets executor: %v", errParse)
		return dialer
	}

	switch setting.Mode {
	case proxyutil.ModeDirect:
		dialer.Proxy = nil
		return dialer
	case proxyutil.ModeProxy:
	default:
		return dialer
	}

	switch setting.URL.Scheme {
	case "socks5", "socks5h":
		var proxyAuth *proxy.Auth
		if setting.URL.User != nil {
			username := setting.URL.User.Username()
			password, _ := setting.URL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		socksDialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("codex websockets executor: create SOCKS5 dialer failed: %v", errSOCKS5)
			return dialer
		}
		dialer.Proxy = nil
		dialer.NetDialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		}
	case "http", "https":
		dialer.Proxy = http.ProxyURL(setting.URL)
	default:
		log.Errorf("codex websockets executor: unsupported proxy scheme: %s", setting.URL.Scheme)
	}

	return dialer
}

func buildCodexResponsesWebsocketURL(httpURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(httpURL))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("codex websockets executor: unsupported responses websocket URL scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("codex websockets executor: responses websocket URL host is empty")
	}
	return parsed.String(), nil
}

func applyCodexPromptCacheHeaders(from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte) ([]byte, http.Header) {
	body, headers, _ := applyCodexPromptCacheHeadersWithContext(context.Background(), from, req, rawJSON)
	return body, headers
}

func applyCodexPromptCacheHeadersWithContext(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte) ([]byte, http.Header, error) {
	headers := http.Header{}
	if len(rawJSON) == 0 {
		return rawJSON, headers, nil
	}

	var cache helps.CodexCache
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		cached, ok, errCache := codexClaudeCodePromptCache(ctx, req)
		if errCache != nil {
			return nil, nil, errCache
		}
		if ok {
			cache = cached
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	}

	if cache.ID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
		setHeaderCasePreserved(headers, "session_id", cache.ID)
		headers.Set("Conversation_id", cache.ID)
	}

	return rawJSON, headers, nil
}

func applyCodexWebsocketHeaders(ctx context.Context, headers http.Header, auth *cliproxyauth.Auth, token string, cfg *config.Config) http.Header {
	if headers == nil {
		headers = http.Header{}
	}
	if strings.TrimSpace(token) != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	var ginHeaders http.Header
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header.Clone()
	}

	isAPIKey := codexAuthUsesAPIKey(auth)
	cfgUserAgent, cfgBetaFeatures := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithPriority(headers, ginHeaders, "x-codex-beta-features", cfgBetaFeatures, "")
	misc.EnsureHeader(headers, ginHeaders, "x-codex-turn-state", "")
	misc.EnsureHeader(headers, ginHeaders, "x-codex-turn-metadata", "")
	misc.EnsureHeader(headers, ginHeaders, "x-client-request-id", "")
	misc.EnsureHeader(headers, ginHeaders, "x-responsesapi-include-timing-metrics", "")
	misc.EnsureHeader(headers, ginHeaders, "Version", "")
	if isAPIKey {
		ensureHeaderWithPriority(headers, ginHeaders, "User-Agent", "", "")
	} else {
		ensureHeaderWithConfigPrecedence(headers, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)
	}

	betaHeader := strings.TrimSpace(headers.Get("OpenAI-Beta"))
	if betaHeader == "" && ginHeaders != nil {
		betaHeader = strings.TrimSpace(ginHeaders.Get("OpenAI-Beta"))
	}
	if betaHeader == "" || !strings.Contains(betaHeader, "responses_websockets=") {
		betaHeader = codexResponsesWebsocketBetaHeaderValue
	}
	headers.Set("OpenAI-Beta", betaHeader)
	sessionFallback := ""
	if strings.Contains(headers.Get("User-Agent"), "Mac OS") {
		sessionFallback = uuid.NewString()
	}
	ensureCodexWebsocketSessionHeader(headers, ginHeaders, sessionFallback)
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		headers.Set("Originator", originator)
	} else if !isAPIKey {
		headers.Set("Originator", codexOriginator)
	}
	if !isAPIKey {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				if trimmed := strings.TrimSpace(accountID); trimmed != "" {
					setHeaderCasePreserved(headers, "ChatGPT-Account-ID", trimmed)
				}
			}
		}
	}

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(&http.Request{Header: headers}, attrs)

	return headers
}

func ensureCodexWebsocketSessionHeader(target http.Header, source http.Header, fallbackValue string) {
	if target == nil {
		return
	}
	sessionID := codexSessionHeaderValue(target)
	if sessionID == "" {
		sessionID = codexSessionHeaderValue(source)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackValue)
	}
	if sessionID != "" {
		setHeaderCasePreserved(target, "session_id", sessionID)
	}
	deleteHeaderCaseInsensitive(target, "Session-Id")
}

func codexSessionHeaderValue(headers http.Header) string {
	for _, key := range []string{"Session-Id", "Session_id", "session_id"} {
		if value := strings.TrimSpace(headerValueCaseInsensitive(headers, key)); value != "" {
			return value
		}
	}
	return ""
}

func codexAuthUsesAPIKey(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["api_key"]) != ""
}

func ensureHeaderCasePreserved(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(headerValueCaseInsensitive(target, key)) != "" {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(headerValueCaseInsensitive(source, key)); val != "" {
			setHeaderCasePreserved(target, key, val)
			return
		}
	}
	if val := strings.TrimSpace(configValue); val != "" {
		setHeaderCasePreserved(target, key, val)
		return
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		setHeaderCasePreserved(target, key, val)
	}
}

func setHeaderCasePreserved(headers http.Header, key string, value string) {
	if headers == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	deleteHeaderCaseInsensitive(headers, key)
	headers[key] = []string{value}
}

func setCodexSessionHeaderCasePreserved(headers http.Header, fallbackKey string, value string) {
	if headers == nil {
		return
	}
	fallbackKey = strings.TrimSpace(fallbackKey)
	value = strings.TrimSpace(value)
	if fallbackKey == "" || value == "" {
		return
	}

	selectedKey := ""
	if _, ok := headers[fallbackKey]; ok && codexSessionHeaderKeyUsesUnderscore(fallbackKey) {
		selectedKey = fallbackKey
	} else {
		for existingKey := range headers {
			if codexSessionHeaderKeyUsesUnderscore(existingKey) {
				selectedKey = existingKey
				break
			}
		}
	}
	if selectedKey == "" {
		selectedKey = fallbackKey
	}
	for existingKey := range headers {
		if codexSessionHeaderKey(existingKey) && existingKey != selectedKey {
			delete(headers, existingKey)
		}
	}
	headers[selectedKey] = []string{value}
}

func codexSessionHeaderKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return normalized == "session_id" || normalized == "session-id"
}

func codexSessionHeaderKeyUsesUnderscore(key string) bool {
	return strings.ToLower(strings.TrimSpace(key)) == "session_id"
}

func headerValueCaseInsensitive(headers http.Header, key string) string {
	key = strings.TrimSpace(key)
	if headers == nil || key == "" {
		return ""
	}
	if val := strings.TrimSpace(headers.Get(key)); val != "" {
		return val
	}
	for existingKey, values := range headers {
		if !strings.EqualFold(existingKey, key) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func deleteHeaderCaseInsensitive(headers http.Header, key string) {
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			delete(headers, existingKey)
		}
	}
}

func codexHeaderDefaults(cfg *config.Config, auth *cliproxyauth.Auth) (string, string) {
	if cfg == nil || auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return "", ""
		}
	}
	return strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent), strings.TrimSpace(cfg.CodexHeaderDefaults.BetaFeatures)
}

func ensureHeaderWithPriority(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if val := strings.TrimSpace(configValue); val != "" {
		target.Set(key, val)
		return
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		target.Set(key, val)
	}
}

func ensureHeaderWithConfigPrecedence(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(configValue); val != "" {
		target.Set(key, val)
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		target.Set(key, val)
	}
}

type statusErrWithHeaders struct {
	statusErr
	headers http.Header
}

func (e statusErrWithHeaders) Headers() http.Header {
	if e.headers == nil {
		return nil
	}
	return e.headers.Clone()
}

func parseCodexWebsocketError(payload []byte) (error, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil, false
	}
	status := int(gjson.GetBytes(payload, "status").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, "status_code").Int())
	}
	if status <= 0 {
		return nil, false
	}

	out := buildCodexWebsocketErrorPayload(payload, status)
	headers := parseCodexWebsocketErrorHeaders(payload)
	statusError := statusErr{code: status, msg: string(out)}
	if retryAfter := parseCodexRetryAfter(status, out, time.Now()); retryAfter != nil {
		statusError.retryAfter = retryAfter
	} else if isCodexWebsocketConnectionLimitError(payload) {
		retryAfter := time.Duration(0)
		statusError.retryAfter = &retryAfter
	}
	return statusErrWithHeaders{
		statusErr: statusError,
		headers:   headers,
	}, true
}

func codexWebsocketStatuslessErrorEvent(payload []byte) (error, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil, false
	}
	if status := int(gjson.GetBytes(payload, "status").Int()); status > 0 {
		return nil, false
	}
	if status := int(gjson.GetBytes(payload, "status_code").Int()); status > 0 {
		return nil, false
	}
	status := http.StatusInternalServerError
	out := buildCodexWebsocketErrorPayload(payload, status)
	return statusErrWithHeaders{
		statusErr: statusErr{code: status, msg: string(out)},
		headers:   parseCodexWebsocketErrorHeaders(payload),
	}, true
}

func buildCodexWebsocketErrorPayload(payload []byte, status int) []byte {
	out := []byte(`{}`)
	out, _ = sjson.SetBytes(out, "status", status)

	if bodyNode := gjson.GetBytes(payload, "body"); bodyNode.Exists() {
		out, _ = sjson.SetRawBytes(out, "body", []byte(bodyNode.Raw))
		if bodyErrorNode := bodyNode.Get("error"); bodyErrorNode.Exists() {
			out, _ = sjson.SetRawBytes(out, "error", []byte(bodyErrorNode.Raw))
			return out
		}
	}

	if errNode := gjson.GetBytes(payload, "error"); errNode.Exists() {
		out, _ = sjson.SetRawBytes(out, "error", []byte(errNode.Raw))
		return out
	}

	out, _ = sjson.SetBytes(out, "error.type", "server_error")
	out, _ = sjson.SetBytes(out, "error.message", http.StatusText(status))
	return out
}

func isCodexWebsocketConnectionLimitError(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	for _, path := range []string{"error.code", "error.type", "body.error.code", "body.error.type", "code", "error"} {
		if strings.TrimSpace(gjson.GetBytes(payload, path).String()) == "websocket_connection_limit_reached" {
			return true
		}
	}
	return false
}

func parseCodexWebsocketErrorHeaders(payload []byte) http.Header {
	headersNode := gjson.GetBytes(payload, "headers")
	if !headersNode.Exists() || !headersNode.IsObject() {
		return nil
	}
	mapped := make(http.Header)
	headersNode.ForEach(func(key, value gjson.Result) bool {
		name := strings.TrimSpace(key.String())
		if name == "" {
			return true
		}
		switch value.Type {
		case gjson.String:
			if v := strings.TrimSpace(value.String()); v != "" {
				mapped.Set(name, v)
			}
		case gjson.Number, gjson.True, gjson.False:
			if v := strings.TrimSpace(value.Raw); v != "" {
				mapped.Set(name, v)
			}
		default:
		}
		return true
	})
	if len(mapped) == 0 {
		return nil
	}
	return mapped
}

func normalizeCodexWebsocketCompletion(payload []byte) []byte {
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.done" {
		updated, err := sjson.SetBytes(payload, "type", "response.completed")
		if err == nil && len(updated) > 0 {
			return updated
		}
	}
	return payload
}

func encodeCodexWebsocketAsSSE(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	line := make([]byte, 0, len("data: ")+len(payload))
	line = append(line, []byte("data: ")...)
	line = append(line, payload...)
	return line
}

func websocketUpgradeRequestLog(info helps.UpstreamRequestLog) helps.UpstreamRequestLog {
	upgradeInfo := info
	upgradeInfo.URL = helps.WebsocketUpgradeRequestURL(info.URL)
	upgradeInfo.Method = http.MethodGet
	upgradeInfo.Body = nil
	upgradeInfo.Headers = info.Headers.Clone()
	if upgradeInfo.Headers == nil {
		upgradeInfo.Headers = make(http.Header)
	}
	if strings.TrimSpace(upgradeInfo.Headers.Get("Connection")) == "" {
		upgradeInfo.Headers.Set("Connection", "Upgrade")
	}
	if strings.TrimSpace(upgradeInfo.Headers.Get("Upgrade")) == "" {
		upgradeInfo.Headers.Set("Upgrade", "websocket")
	}
	return upgradeInfo
}

func recordAPIWebsocketHandshake(ctx context.Context, cfg *config.Config, resp *http.Response) {
	if resp == nil {
		return
	}
	helps.RecordAPIWebsocketHandshake(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
}

func websocketHandshakeBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
	if len(body) == 0 {
		return nil
	}
	return body
}

func closeHTTPResponseBody(resp *http.Response, logPrefix string) {
	if resp == nil || resp.Body == nil {
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("%s: %v", logPrefix, errClose)
	}
}

func executionSessionIDFromOptions(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func (e *CodexWebsocketsExecutor) getOrCreateSession(sessionID string) *codexWebsocketSession {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if e == nil {
		return nil
	}
	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions == nil {
		store.sessions = make(map[string]*codexWebsocketSession)
	}
	if sess, ok := store.sessions[sessionID]; ok && sess != nil {
		return sess
	}
	sess := &codexWebsocketSession{
		sessionID:            sessionID,
		upstreamDisconnectCh: make(chan error, 1),
	}
	store.sessions[sessionID] = sess
	return sess
}

func (e *CodexWebsocketsExecutor) UpstreamDisconnectChan(sessionID string) <-chan error {
	sess := e.getOrCreateSession(sessionID)
	if sess == nil {
		return nil
	}
	return sess.upstreamDisconnectCh
}

func (e *CodexWebsocketsExecutor) UpstreamSessionActive(sessionID string) bool {
	sess := e.getSession(sessionID)
	return sess != nil && sess.hasUpstreamConn()
}

func (e *CodexWebsocketsExecutor) getSession(sessionID string) *codexWebsocketSession {
	sessionID = strings.TrimSpace(sessionID)
	if e == nil || sessionID == "" {
		return nil
	}
	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.sessions[sessionID]
}

func (e *CodexWebsocketsExecutor) ensureUpstreamConn(ctx context.Context, auth *cliproxyauth.Auth, sess *codexWebsocketSession, authID string, wsURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	if sess == nil {
		return e.dialCodexWebsocket(ctx, auth, wsURL, headers)
	}

	sess.connMu.Lock()
	conn := sess.conn
	readerConn := sess.readerConn
	sess.connMu.Unlock()
	if conn != nil {
		if readerConn != conn {
			sess.connMu.Lock()
			sess.readerConn = conn
			sess.connMu.Unlock()
			sess.configureConn(conn)
			go e.readUpstreamLoop(sess, conn)
		}
		return conn, nil, nil
	}

	conn, resp, errDial := e.dialCodexWebsocket(ctx, auth, wsURL, headers)
	if errDial != nil {
		return nil, resp, errDial
	}

	sess.connMu.Lock()
	if sess.conn != nil {
		previous := sess.conn
		sess.connMu.Unlock()
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("codex websockets executor: close websocket error: %v", errClose)
		}
		return previous, nil, nil
	}
	sess.conn = conn
	sess.wsURL = wsURL
	sess.authID = authID
	sess.readerConn = conn
	sess.connMu.Unlock()

	sess.configureConn(conn)
	go e.readUpstreamLoop(sess, conn)
	logCodexWebsocketConnected(sess.sessionID, authID, wsURL)
	return conn, resp, nil
}

func (e *CodexWebsocketsExecutor) readUpstreamLoop(sess *codexWebsocketSession, conn *websocket.Conn) {
	if e == nil || sess == nil || conn == nil {
		return
	}
	var terminalDeliveredCh chan codexWebsocketRead
	for {
		_ = conn.SetReadDeadline(time.Now().Add(codexResponsesWebsocketIdleTimeout))
		msgType, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			if sess.shouldSuppressReadErrorForConn(conn) {
				e.clearUpstreamConn(sess, conn, "upstream_disconnected_suppressed", errRead, false)
				return
			}
			if terminalDeliveredCh != nil {
				sess.markLostTerminalStateForConn(conn)
				e.clearUpstreamConn(sess, conn, "upstream_disconnected_after_terminal", errRead, false)
				if sess.activeChannelUnchangedOrCleared(terminalDeliveredCh) {
					return
				}
				_ = sess.deliverActiveRead(codexWebsocketRead{conn: conn, err: codexWebsocketStaleTerminalCloseError(errRead)})
				return
			}
			if !sess.deliverActiveRead(codexWebsocketRead{conn: conn, err: errRead}) {
				if isCodexWebsocketMessageTooBigError(errRead) {
					e.clearUpstreamConn(sess, conn, "message_too_big_without_active_request", errRead, false)
					return
				}
				e.invalidateUpstreamConn(sess, conn, "upstream_disconnected", errRead)
			}
			return
		}

		if msgType != websocket.TextMessage {
			if msgType == websocket.BinaryMessage {
				errBinary := fmt.Errorf("codex websockets executor: unexpected binary message")
				if !sess.deliverActiveRead(codexWebsocketRead{conn: conn, err: errBinary}) {
					e.invalidateUpstreamConn(sess, conn, "unexpected_binary", errBinary)
				}
				return
			}
			continue
		}

		sess.activeMu.Lock()
		ch := sess.activeCh
		done := sess.activeDone
		sess.activeMu.Unlock()
		if ch == nil {
			continue
		}
		delivered := false
		select {
		case ch <- codexWebsocketRead{conn: conn, msgType: msgType, payload: payload}:
			delivered = true
		case <-done:
		}
		if delivered {
			if isCodexWebsocketTerminalPayload(payload) {
				sess.markTerminalStateConn(conn)
				sess.stopActiveDelivery(ch)
				terminalDeliveredCh = ch
			} else {
				terminalDeliveredCh = nil
			}
		}
	}
}

func (e *CodexWebsocketsExecutor) invalidateUpstreamConn(sess *codexWebsocketSession, conn *websocket.Conn, reason string, err error) {
	e.clearUpstreamConn(sess, conn, reason, err, true)
}

func (e *CodexWebsocketsExecutor) failCodexWebsocketRequestWithoutUpstreamContext(ctx context.Context, sess *codexWebsocketSession, conn *websocket.Conn, reason string, cause error) error {
	err := errCodexWebsocketRequestWithoutUpstreamContext
	if cause != nil {
		err = fmt.Errorf("%w: %w", errCodexWebsocketRequestWithoutUpstreamContext, cause)
	}
	helps.RecordAPIWebsocketError(ctx, e.cfg, reason, err)
	if conn != nil {
		e.invalidateUpstreamConn(sess, conn, reason, err)
		return err
	}
	if sess != nil {
		sess.notifyUpstreamDisconnect(err)
	}
	return err
}

func (e *CodexWebsocketsExecutor) clearUpstreamConn(sess *codexWebsocketSession, conn *websocket.Conn, reason string, err error, notify bool) {
	if sess == nil || conn == nil {
		return
	}

	sess.connMu.Lock()
	current := sess.conn
	authID := sess.authID
	wsURL := sess.wsURL
	sessionID := sess.sessionID
	if sess.suppressReadErrorConn == conn {
		sess.suppressReadErrorConn = nil
	}
	if current == nil || current != conn {
		sess.connMu.Unlock()
		return
	}
	sess.conn = nil
	if sess.readerConn == conn {
		sess.readerConn = nil
	}
	sess.connMu.Unlock()

	logCodexWebsocketDisconnected(sessionID, authID, wsURL, reason, err)
	if notify {
		sess.notifyUpstreamDisconnect(err)
	}
	if errClose := conn.Close(); errClose != nil {
		log.Errorf("codex websockets executor: close websocket error: %v", errClose)
	}
}

func (e *CodexWebsocketsExecutor) CloseExecutionSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if e == nil {
		return
	}
	if sessionID == "" {
		return
	}
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		// Executor replacement can happen during hot reload (config/credential changes).
		// Do not force-close upstream websocket sessions here, otherwise in-flight
		// downstream websocket requests get interrupted.
		return
	}

	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	sess := store.sessions[sessionID]
	delete(store.sessions, sessionID)
	store.mu.Unlock()

	e.closeExecutionSession(sess, "session_closed")
}

func (e *CodexWebsocketsExecutor) closeAllExecutionSessions(reason string) {
	if e == nil {
		return
	}

	store := e.store
	if store == nil {
		store = globalCodexWebsocketSessionStore
	}
	store.mu.Lock()
	sessions := make([]*codexWebsocketSession, 0, len(store.sessions))
	for sessionID, sess := range store.sessions {
		delete(store.sessions, sessionID)
		if sess != nil {
			sessions = append(sessions, sess)
		}
	}
	store.mu.Unlock()

	for i := range sessions {
		e.closeExecutionSession(sessions[i], reason)
	}
}

func (e *CodexWebsocketsExecutor) closeExecutionSession(sess *codexWebsocketSession, reason string) {
	closeCodexWebsocketSession(sess, reason)
}

func closeCodexWebsocketSession(sess *codexWebsocketSession, reason string) {
	if sess == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "session_closed"
	}

	sess.connMu.Lock()
	conn := sess.conn
	authID := sess.authID
	wsURL := sess.wsURL
	sess.conn = nil
	if sess.readerConn == conn {
		sess.readerConn = nil
	}
	sessionID := sess.sessionID
	sess.connMu.Unlock()

	if conn == nil {
		return
	}
	logCodexWebsocketDisconnected(sessionID, authID, wsURL, reason, nil)
	if errClose := conn.Close(); errClose != nil {
		log.Errorf("codex websockets executor: close websocket error: %v", errClose)
	}
}

func logCodexWebsocketConnected(sessionID string, authID string, wsURL string) {
	log.Infof("codex websockets: upstream connected session=%s auth=%s url=%s", strings.TrimSpace(sessionID), strings.TrimSpace(authID), strings.TrimSpace(wsURL))
}

func logCodexWebsocketDisconnected(sessionID string, authID string, wsURL string, reason string, err error) {
	if err != nil {
		log.Infof("codex websockets: upstream disconnected session=%s auth=%s url=%s reason=%s err=%v", strings.TrimSpace(sessionID), strings.TrimSpace(authID), strings.TrimSpace(wsURL), strings.TrimSpace(reason), err)
		return
	}
	log.Infof("codex websockets: upstream disconnected session=%s auth=%s url=%s reason=%s", strings.TrimSpace(sessionID), strings.TrimSpace(authID), strings.TrimSpace(wsURL), strings.TrimSpace(reason))
}

// CloseCodexWebsocketSessionsForAuthID closes all active Codex upstream websocket sessions
// associated with the supplied auth ID.
func CloseCodexWebsocketSessionsForAuthID(authID string, reason string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "auth_removed"
	}

	store := globalCodexWebsocketSessionStore
	if store == nil {
		return
	}

	type sessionItem struct {
		sessionID string
		sess      *codexWebsocketSession
	}

	store.mu.Lock()
	items := make([]sessionItem, 0, len(store.sessions))
	for sessionID, sess := range store.sessions {
		items = append(items, sessionItem{sessionID: sessionID, sess: sess})
	}
	store.mu.Unlock()

	matches := make([]sessionItem, 0)
	for i := range items {
		sess := items[i].sess
		if sess == nil {
			continue
		}
		sess.connMu.Lock()
		sessAuthID := strings.TrimSpace(sess.authID)
		sess.connMu.Unlock()
		if sessAuthID == authID {
			matches = append(matches, items[i])
		}
	}
	if len(matches) == 0 {
		return
	}

	toClose := make([]*codexWebsocketSession, 0, len(matches))
	store.mu.Lock()
	for i := range matches {
		current, ok := store.sessions[matches[i].sessionID]
		if !ok || current == nil || current != matches[i].sess {
			continue
		}
		delete(store.sessions, matches[i].sessionID)
		toClose = append(toClose, current)
	}
	store.mu.Unlock()

	for i := range toClose {
		closeCodexWebsocketSession(toClose[i], reason)
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
	return e.httpExec.Execute(ctx, auth, req, opts)
}

func (e *CodexAutoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.httpExec == nil || e.wsExec == nil {
		return nil, fmt.Errorf("codex auto executor: executor is nil")
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) && codexWebsocketsEnabled(auth) {
		return e.wsExec.ExecuteStream(ctx, auth, req, opts)
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

func (e *CodexAutoExecutor) UpstreamSessionActive(sessionID string) bool {
	if e == nil || e.wsExec == nil {
		return false
	}
	return e.wsExec.UpstreamSessionActive(sessionID)
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
