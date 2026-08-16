package openai

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	wsRequestTypeCreate                   = "response.create"
	wsRequestTypeAppend                   = "response.append"
	wsEventTypeError                      = "error"
	wsEventTypeCompleted                  = "response.completed"
	wsEventTypeDone                       = "response.done"
	wsDoneMarker                          = "[DONE]"
	wsTurnStateHeader                     = "x-codex-turn-state"
	wsTimelineBodyKey                     = "WEBSOCKET_TIMELINE_OVERRIDE"
	wsCloseReasonMaxBytes                 = 123
	wsHTTPReplayRequiredCloseReason       = "upstream requires HTTP replay"
	responsesWebsocketUpstreamModeUnknown = ""
	responsesWebsocketUpstreamModeWS      = "websocket"
	responsesWebsocketUpstreamModeHTTP    = "http"

	codexLocalCompactionSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"
)

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// writeWebsocketCloseForUpstreamError mirrors transport-level upstream close
// codes to the downstream WebSocket client before the connection is torn down.
// Without this the client only observes an abnormal closure (1006) and cannot
// apply its own close-code based handling (e.g. falling back to SSE on 1009).
func writeWebsocketCloseForUpstreamError(conn *websocket.Conn, err error) (bool, error) {
	if conn == nil {
		return false, nil
	}
	matched, payload := websocketClosePayloadForUpstreamError(err)
	if !matched {
		return false, nil
	}
	return true, conn.WriteControl(websocket.CloseMessage, payload, time.Time{})
}

func websocketClosePayloadForUpstreamError(err error) (bool, []byte) {
	if err == nil {
		return false, nil
	}

	errText := err.Error()
	if cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) {
		return true, websocket.FormatCloseMessage(
			websocket.CloseServiceRestart,
			truncateWebsocketCloseReason(wsHTTPReplayRequiredCloseReason, wsCloseReasonMaxBytes),
		)
	}

	code := 0
	reason := ""
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig {
		code = closeErr.Code
		reason = closeErr.Text
	} else {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusRequestEntityTooLarge ||
			gjson.Get(errText, "error.code").String() != "message_too_big" {
			return false, nil
		}
		code = websocket.CloseMessageTooBig
		reason = strings.TrimSpace(gjson.Get(errText, "error.message").String())
	}
	if reason == "" {
		reason = "message too big"
	}
	reason = truncateWebsocketCloseReason(reason, wsCloseReasonMaxBytes)
	return true, websocket.FormatCloseMessage(code, reason)
}

type responsesWebsocketWriter struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	closing atomic.Bool
}

func newResponsesWebsocketWriter(conn *websocket.Conn) *responsesWebsocketWriter {
	return &responsesWebsocketWriter{conn: conn}
}

// closeForUpstreamError sends a best-effort close frame without waiting behind
// an active downstream data writer. If a data write already owns writeMu, the
// connection is closed immediately so the blocked writer and session can exit.
func (w *responsesWebsocketWriter) closeForUpstreamError(err error) (bool, error) {
	if w == nil || w.conn == nil {
		return false, nil
	}
	matched, payload := websocketClosePayloadForUpstreamError(err)
	if !matched {
		return false, nil
	}
	if !w.closing.CompareAndSwap(false, true) {
		return true, nil
	}
	if !w.writeMu.TryLock() {
		return true, w.conn.Close()
	}
	defer w.writeMu.Unlock()

	errWrite := w.conn.WriteControl(websocket.CloseMessage, payload, time.Time{})
	errClose := w.conn.Close()
	if errWrite != nil {
		return true, errWrite
	}
	return true, errClose
}

func (w *responsesWebsocketWriter) closeWithoutError() (bool, error) {
	if w == nil || w.conn == nil {
		return false, nil
	}
	if !w.closing.CompareAndSwap(false, true) {
		return false, nil
	}
	return true, w.conn.Close()
}

func (w *responsesWebsocketWriter) closeWithPayload(payload []byte) (bool, error) {
	if w == nil || w.conn == nil {
		return false, nil
	}
	if !w.closing.CompareAndSwap(false, true) {
		return false, nil
	}
	if !w.writeMu.TryLock() {
		return false, w.conn.Close()
	}
	defer w.writeMu.Unlock()

	errWrite := w.conn.WriteMessage(websocket.TextMessage, payload)
	errClose := w.conn.Close()
	if errWrite != nil {
		return false, errWrite
	}
	return true, errClose
}

func (w *responsesWebsocketWriter) closeForUpstreamDisconnect(err error) {
	if w == nil || w.conn == nil {
		return
	}
	if matched, _ := w.closeForUpstreamError(err); matched {
		return
	}

	errMsg := handlers.ExecutionErrorMessage(err)
	if !shouldExposeResponsesUpstreamError(errMsg) {
		_, _ = w.closeWithoutError()
		return
	}
	payload, errBuild := buildResponsesWebsocketErrorPayload(errMsg)
	if errBuild != nil {
		_, _ = w.closeWithoutError()
		return
	}
	wrote, errClose := w.closeWithPayload(payload)
	if wrote {
		log.Infof(
			"responses websocket: downstream_out disconnect_error event=%s payload=%s",
			websocketPayloadEventType(payload),
			websocketPayloadPreview(payload),
		)
	}
	if errClose != nil && !errors.Is(errClose, websocket.ErrCloseSent) {
		log.Debugf("responses websocket: upstream disconnect close failed: %v", errClose)
	}
}

// isWebsocketConnectionClosedError reports whether the error only means the
// connection was already torn down. These are expected during shutdown races
// (the proxy closes after sending a terminal frame, or the client hangs up mid
// write) and must not be logged as proxy failures.
func isWebsocketConnectionClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, websocket.ErrCloseSent) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

func truncateWebsocketCloseReason(reason string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(reason) <= maxBytes && utf8.ValidString(reason) {
		return reason
	}

	// Decode from the front so work and output stay bounded by maxBytes.
	var truncated strings.Builder
	truncated.Grow(min(len(reason), maxBytes))
	remaining := maxBytes
	runeErrorSize := utf8.RuneLen(utf8.RuneError)
	for len(reason) > 0 && remaining > 0 {
		r, size := utf8.DecodeRuneInString(reason)
		if r == utf8.RuneError && size == 1 {
			if runeErrorSize > remaining {
				break
			}
			truncated.WriteRune(utf8.RuneError)
			reason = reason[1:]
			remaining -= runeErrorSize
			continue
		}
		if size > remaining {
			break
		}
		truncated.WriteString(reason[:size])
		reason = reason[size:]
		remaining -= size
	}
	return truncated.String()
}

// ResponsesWebsocket handles websocket requests for /v1/responses.
// It accepts `response.create` and `response.append` requests and streams
// response events back as JSON websocket text messages.
func (h *OpenAIResponsesAPIHandler) ResponsesWebsocket(c *gin.Context) {
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, websocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	writer := newResponsesWebsocketWriter(conn)
	passthroughSessionID := uuid.NewString()
	downstreamSessionKey := websocketDownstreamSessionKey(c.Request)
	retainResponsesWebsocketToolCaches(downstreamSessionKey)
	clientIP := websocketClientAddress(c)
	log.Infof("responses websocket: client connected id=%s remote=%s", passthroughSessionID, clientIP)

	requestLogEnabled := h != nil && h.Cfg != nil && h.Cfg.RequestLog
	wsTimelineLog := newWebsocketTimelineLog(requestLogEnabled, websocketTimelineSourceFromContext(c))

	wsDone := make(chan struct{})
	defer close(wsDone)

	if h != nil && h.AuthManager != nil {
		type upstreamDisconnectSubscriber interface {
			UpstreamDisconnectChan(sessionID string) <-chan error
		}
		for _, provider := range []string{"codex", "xai"} {
			exec, ok := h.AuthManager.Executor(provider)
			if !ok || exec == nil {
				continue
			}
			if subscriber, ok := exec.(upstreamDisconnectSubscriber); ok && subscriber != nil {
				disconnectCh := subscriber.UpstreamDisconnectChan(passthroughSessionID)
				if disconnectCh != nil {
					go func() {
						select {
						case <-wsDone:
							return
						case disconnectErr := <-disconnectCh:
							writer.closeForUpstreamDisconnect(disconnectErr)
						}
					}()
				}
			}
		}
	}

	var wsTerminateErr error
	defer func() {
		releaseResponsesWebsocketToolCaches(downstreamSessionKey)
		if wsTerminateErr != nil {
			appendWebsocketTimelineDisconnect(wsTimelineLog, wsTerminateErr, time.Now())
			// log.Infof("responses websocket: session closing id=%s reason=%v", passthroughSessionID, wsTerminateErr)
		} else {
			log.Infof("responses websocket: session closing id=%s", passthroughSessionID)
		}
		if h != nil && h.AuthManager != nil {
			h.AuthManager.CloseExecutionSession(passthroughSessionID)
			log.Infof("responses websocket: upstream execution session closed id=%s", passthroughSessionID)
		}
		wsTimelineLog.SetContext(c)
		if errClose := conn.Close(); errClose != nil && !isWebsocketConnectionClosedError(errClose) {
			log.Warnf("responses websocket: close connection error: %v", errClose)
		}
	}()

	var lastRequest []byte
	lastResponseOutput := []byte("[]")
	lastResponseID := ""
	var lastResponsePendingToolCallIDs []string
	pinnedAuthID := ""
	// Preserve independent upstream auth affinity when a downstream session switches providers.
	pinnedAuthByProvider := make(map[string]responsesWebsocketPinnedAuthState)
	passthroughModelName := ""
	upstreamMode := responsesWebsocketUpstreamModeUnknown
	upstreamWebsocketAuthID := ""
	sessionAuthByIDWithSource := func(authID string) (*coreauth.Auth, bool, bool) {
		if h == nil || h.AuthManager == nil {
			return nil, false, false
		}
		// Prefer the current manager view so hot-reloaded transport eligibility is
		// observed even when the execution session still holds an older auth snapshot.
		if auth, ok := h.AuthManager.GetByID(authID); ok {
			return auth, false, true
		}
		if auth, ok := h.AuthManager.GetExecutionSessionAuthByID(passthroughSessionID, authID); ok {
			return auth, true, true
		}
		return nil, false, false
	}
	sessionAuthByID := func(authID string) (*coreauth.Auth, bool) {
		auth, _, ok := sessionAuthByIDWithSource(authID)
		return auth, ok
	}
	upstreamModeForAuth := func(auth *coreauth.Auth) string {
		if auth != nil && websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata) {
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if provider == "codex" || provider == "xai" {
				return responsesWebsocketUpstreamModeWS
			}
		}
		return responsesWebsocketUpstreamModeHTTP
	}
	rememberPinnedAuth := func(authID string, modelName string) {
		authID = strings.TrimSpace(authID)
		auth, ok := sessionAuthByID(authID)
		if authID == "" || !ok || auth == nil {
			return
		}
		pinnedAuthID = authID
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		_, modelKey := responsesWebsocketProviderSetForModel(responsesWebsocketResolvedModelName(modelName))
		if providerKey != "" {
			pinnedAuthByProvider[providerKey] = responsesWebsocketPinnedAuthState{authID: authID, modelKey: modelKey}
		}
	}
	forgetPinnedAuth := func() {
		for providerKey, state := range pinnedAuthByProvider {
			if state.authID == pinnedAuthID {
				delete(pinnedAuthByProvider, providerKey)
			}
		}
		pinnedAuthID = ""
	}

	for {
		msgType, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			wsTerminateErr = errReadMessage
			if websocket.IsCloseError(errReadMessage, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				log.Infof("responses websocket: client disconnected id=%s error=%v", passthroughSessionID, errReadMessage)
			} else {
				// log.Warnf("responses websocket: read message failed id=%s error=%v", passthroughSessionID, errReadMessage)
			}
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		// log.Infof(
		// 	"responses websocket: downstream_in id=%s type=%d event=%s payload=%s",
		// 	passthroughSessionID,
		// 	msgType,
		// 	websocketPayloadEventType(payload),
		// 	websocketPayloadPreview(payload),
		// )
		wsTimelineLog.BeginRequest()
		wsTimelineLog.Append("request", payload, time.Now())

		explicitRequestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
		requestModelName := explicitRequestModelName
		if requestModelName == "" {
			requestModelName = passthroughModelName
		}
		if requestModelName == "" {
			requestModelName = strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		}
		executionParent := context.WithValue(c.Request.Context(), "gin", c)
		executionParent, routeOverridesModelResolution := h.PrepareStreamModelRoute(
			executionParent,
			h.HandlerType(),
			requestModelName,
			payload,
		)
		if pinnedAuthID != "" {
			pinnedAuth, homeRuntime, ok := sessionAuthByIDWithSource(pinnedAuthID)
			providerKey := ""
			if pinnedAuth != nil {
				providerKey = strings.ToLower(strings.TrimSpace(pinnedAuth.Provider))
			}
			state, hasState := pinnedAuthByProvider[providerKey]
			if !ok || !hasState || state.authID != pinnedAuthID || !responsesWebsocketPinnedAuthMatchesModel(pinnedAuth, requestModelName, state.modelKey, homeRuntime) {
				pinnedAuthID = ""
			}
		}
		if pinnedAuthID == "" {
			providerSet, _ := responsesWebsocketProviderSetForModel(responsesWebsocketResolvedModelName(requestModelName))
			if len(providerSet) == 1 {
				for providerKey := range providerSet {
					state, ok := pinnedAuthByProvider[providerKey]
					candidateAuth, homeRuntime, okAuth := sessionAuthByIDWithSource(state.authID)
					if ok && okAuth && responsesWebsocketPinnedAuthMatchesModel(candidateAuth, requestModelName, state.modelKey, homeRuntime) {
						pinnedAuthID = state.authID
					} else {
						delete(pinnedAuthByProvider, providerKey)
					}
				}
			}
		}
		useUpstreamWebsocketPassthrough := h.responsesWebsocketUsesUpstreamWebsocketPassthrough(requestModelName)
		if pinnedAuthID != "" {
			if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && responsesWebsocketAuthSupportsIncrementalInput(pinnedAuth) {
				provider := strings.ToLower(strings.TrimSpace(pinnedAuth.Provider))
				useUpstreamWebsocketPassthrough = provider == "codex" || provider == "xai"
			}
		}
		nativeWebsocketPassthrough := !routeOverridesModelResolution && responsesWebsocketNativePassthroughAllowed(
			upstreamMode,
			useUpstreamWebsocketPassthrough,
			pinnedAuthID,
			upstreamWebsocketAuthID,
		)
		requestRequiresCurrentUpstreamWebsocket := responsesWebsocketRequestRequiresCurrentUpstream(payload)
		if upstreamMode == responsesWebsocketUpstreamModeWS && !nativeWebsocketPassthrough {
			if requestRequiresCurrentUpstreamWebsocket {
				replayErr := responsesWebsocketHTTPReplayRequiredError()
				wsTerminateErr = replayErr
				matched, errClose := writer.closeForUpstreamError(replayErr)
				if !matched {
					_ = conn.Close()
				} else if errClose != nil && !errors.Is(errClose, websocket.ErrCloseSent) {
					log.Debugf("responses websocket: replay close failed id=%s error=%v", passthroughSessionID, errClose)
				}
				return
			}
			// A full response.create is already a self-contained reset and can safely
			// establish a new upstream transport without another replay.
		}
		if explicitRequestModelName != "" && !useUpstreamWebsocketPassthrough {
			passthroughModelName = ""
		}

		allowCompactionReplayBypass := false
		if !nativeWebsocketPassthrough {
			if pinnedAuthID != "" {
				if pinnedAuth, ok := sessionAuthByID(pinnedAuthID); ok && pinnedAuth != nil {
					allowCompactionReplayBypass = responsesWebsocketAuthSupportsCompactionReplay(pinnedAuth)
				}
			} else {
				allowCompactionReplayBypass = h.websocketUpstreamSupportsCompactionReplayForModel(requestModelName)
			}
		}

		var requestJSON []byte
		var updatedLastRequest []byte
		var errMsg *interfaces.ErrorMessage
		if nativeWebsocketPassthrough {
			requestJSON, errMsg = normalizeResponsesWebsocketPassthroughRequest(payload, requestModelName)
		} else if len(lastRequest) == 0 && strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" {
			errMsg = responsesWebsocketPreviousResponseNotFoundError()
		} else {
			requestJSON, updatedLastRequest, errMsg = normalizeResponsesWebsocketRequestWithIncrementalState(
				payload,
				lastRequest,
				lastResponseOutput,
				lastResponseID,
				lastResponsePendingToolCallIDs,
				false,
				allowCompactionReplayBypass,
			)
		}
		if errMsg != nil {
			h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
			markAPIResponseTimestamp(c)
			errorPayload, errWrite := writeResponsesWebsocketError(writer, wsTimelineLog, errMsg)
			log.Infof(
				"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
				passthroughSessionID,
				websocket.TextMessage,
				websocketPayloadEventType(errorPayload),
				websocketPayloadPreview(errorPayload),
			)
			if errWrite != nil {
				log.Warnf(
					"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					passthroughSessionID,
					websocketPayloadEventType(errorPayload),
					errWrite,
				)
				return
			}
			continue
		}

		requestJSON = h.prepareCodexMultiAgentV2Tools(c, requestJSON)

		if !useUpstreamWebsocketPassthrough && shouldHandleResponsesWebsocketPrewarmLocally(payload, lastRequest, false) {
			if updated, errDelete := sjson.DeleteBytes(requestJSON, "generate"); errDelete == nil {
				requestJSON = updated
			}
			if updated, errDelete := sjson.DeleteBytes(updatedLastRequest, "generate"); errDelete == nil {
				updatedLastRequest = updated
			}
			lastRequest = updatedLastRequest
			lastResponseOutput = []byte("[]")
			lastResponseID = ""
			lastResponsePendingToolCallIDs = nil
			if errWrite := writeResponsesWebsocketSyntheticPrewarm(c, writer, requestJSON, wsTimelineLog, passthroughSessionID); errWrite != nil {
				wsTerminateErr = errWrite
				return
			}
			continue
		}

		var toolCacheTurn *responsesWebsocketToolCacheTurn
		nextLastRequest := lastRequest
		if nativeWebsocketPassthrough {
			if modelName := strings.TrimSpace(gjson.GetBytes(requestJSON, "model").String()); modelName != "" {
				passthroughModelName = modelName
			}
		} else {
			requestJSON, toolCacheTurn = prepareResponsesWebsocketFallbackTurn(downstreamSessionKey, requestJSON)
			nextLastRequest = requestJSON
		}

		modelName := gjson.GetBytes(requestJSON, "model").String()
		lastAttemptedAuthID := pinnedAuthID
		attemptedUpstreamMode := responsesWebsocketUpstreamModeUnknown
		selectedAuthObserved := false
		pinnedAuthAttempted := false
		cliCtx, cliCancel := h.GetContextWithCancel(h, c, executionParent)
		cliCtx = cliproxyexecutor.WithDownstreamWebsocket(cliCtx)
		if nativeWebsocketPassthrough && requestRequiresCurrentUpstreamWebsocket {
			cliCtx = cliproxyexecutor.WithRequiredUpstreamWebsocket(cliCtx)
		}
		cliCtx = handlers.WithExecutionSessionID(cliCtx, passthroughSessionID)
		cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
			authID = strings.TrimSpace(authID)
			if authID == "" || h == nil || h.AuthManager == nil {
				return
			}
			lastAttemptedAuthID = authID
			selectedAuthObserved = true
			pinnedAuthAttempted = pinnedAuthAttempted || (pinnedAuthID != "" && authID == pinnedAuthID)
			selectedAuth, ok := sessionAuthByID(authID)
			if !ok || selectedAuth == nil {
				return
			}
			attemptedUpstreamMode = upstreamModeForAuth(selectedAuth)
		})
		if pinnedAuthID != "" && !routeOverridesModelResolution {
			cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
		}
		dataChan, _, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, requestJSON, "")
		if !selectedAuthObserved {
			// Plugin/alternate routes bypass auth selection. Keep canonical HTTP-mode
			// state instead of inheriting the previous pinned websocket mode.
			attemptedUpstreamMode = responsesWebsocketUpstreamModeHTTP
		}
		// A connection-scoped continuation cannot rotate credentials in place. Suppress
		// credential errors and make the client replay the full turn on a new socket.
		replayPinnedAuthFailure := func(errMsg *interfaces.ErrorMessage) bool {
			return nativeWebsocketPassthrough && requestRequiresCurrentUpstreamWebsocket && pinnedAuthAttempted &&
				shouldReplayResponsesWebsocketPinnedAuthFailure(errMsg)
		}

		completedOutput, completedResponseID, completedPendingToolCallIDs, forwardErrMsg, errForward := h.forwardResponsesWebsocket(
			c,
			writer,
			cliCancel,
			dataChan,
			errChan,
			wsTimelineLog,
			passthroughSessionID,
			responsesWebsocketForwardOptions{
				toolCacheTurn: toolCacheTurn,
				suppressError: replayPinnedAuthFailure,
			},
		)
		if errForward != nil {
			wsTerminateErr = errForward
			switch {
			case errors.Is(errForward, websocket.ErrCloseSent):
			case isWebsocketConnectionClosedError(errForward):
				// The client hung up while a downstream write was in flight. This is a
				// normal shutdown race, not a proxy failure.
				log.Debugf("responses websocket: client closed during forward id=%s error=%v", passthroughSessionID, errForward)
			default:
				log.Warnf("responses websocket: forward failed id=%s error=%v", passthroughSessionID, errForward)
			}
			return
		}
		if forwardErrMsg != nil {
			if pinnedAuthAttempted && shouldReleaseResponsesWebsocketPinnedAuth(forwardErrMsg) {
				forgetPinnedAuth()
			}
			if replayPinnedAuthFailure(forwardErrMsg) {
				replayErr := responsesWebsocketHTTPReplayRequiredError()
				wsTerminateErr = replayErr
				matched, errClose := writer.closeForUpstreamError(replayErr)
				if !matched {
					_ = conn.Close()
				} else if errClose != nil && !errors.Is(errClose, websocket.ErrCloseSent) {
					log.Debugf("responses websocket: credential replay close failed id=%s error=%v", passthroughSessionID, errClose)
				}
				return
			}
			continue
		}

		toolCacheTurn.commit()
		upstreamMode = attemptedUpstreamMode
		if upstreamMode == responsesWebsocketUpstreamModeWS {
			upstreamWebsocketAuthID = lastAttemptedAuthID
			if lastAttemptedAuthID != "" {
				rememberPinnedAuth(lastAttemptedAuthID, modelName)
			}
			passthroughModelName = modelName
			lastRequest = nil
			lastResponseOutput = []byte("[]")
			lastResponseID = ""
			lastResponsePendingToolCallIDs = nil
		} else {
			upstreamWebsocketAuthID = ""
			lastRequest = nextLastRequest
			lastResponseOutput = completedOutput
			lastResponseID = strings.TrimSpace(completedResponseID)
			lastResponsePendingToolCallIDs = append([]string(nil), completedPendingToolCallIDs...)
		}
	}
}

func responsesWebsocketHTTPReplayRequiredError() error {
	return cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
}

func responsesWebsocketRequestRequiresCurrentUpstream(payload []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != "" ||
		strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == wsRequestTypeAppend
}

func responsesWebsocketNativePassthroughAllowed(upstreamMode string, useUpstreamWebsocket bool, pinnedAuthID string, upstreamAuthID string) bool {
	return upstreamMode == responsesWebsocketUpstreamModeWS && useUpstreamWebsocket &&
		strings.TrimSpace(pinnedAuthID) != "" && strings.TrimSpace(pinnedAuthID) == strings.TrimSpace(upstreamAuthID)
}

func websocketClientAddress(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.ClientIP())
}

func websocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}

	// Keep the same sticky turn-state across reconnects when provided by the client.
	turnState := strings.TrimSpace(req.Header.Get(wsTurnStateHeader))
	if turnState != "" {
		headers.Set(wsTurnStateHeader, turnState)
	}
	return headers
}

func responsesWebsocketPreviousResponseNotFoundError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusConflict,
		Error: errors.New(
			`{"error":{"message":"Previous response is not available on this websocket; resend the full conversation input without previous_response_id","type":"invalid_request_error","code":"previous_response_not_found","param":"previous_response_id"}}`,
		),
	}
}
