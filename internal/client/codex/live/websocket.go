package live

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const defaultStandardRealtimeModel = "gpt-realtime"

// HandleRealtimeWebsocket dispatches a standard Realtime WebSocket or an existing call sideband.
func (h *Handler) HandleRealtimeWebsocket(c *gin.Context) {
	if strings.TrimSpace(c.Query("call_id")) != "" {
		h.HandleSideband(c)
		return
	}
	h.HandleDirectWebsocket(c)
}

// HandleDirectWebsocket relays a standard Realtime WebSocket through Codex OAuth.
func (h *Handler) HandleDirectWebsocket(c *gin.Context) {
	if h == nil || h.authManager == nil {
		writeRealtimeError(c, http.StatusServiceUnavailable, "Codex auth manager unavailable", "server_error", "codex_auth_unavailable")
		return
	}
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.Header("Upgrade", "websocket")
		writeRealtimeError(c, http.StatusUpgradeRequired, "WebSocket upgrade required", "invalid_request_error", "websocket_upgrade_required")
		return
	}

	requestedModel := strings.TrimSpace(c.Query("model"))
	if requestedModel == "" {
		requestedModel = defaultStandardRealtimeModel
	}
	selectionModel := codexRealtimeModel(requestedModel)
	tokenSession := clientSecretSession(c)
	if len(tokenSession) > 0 {
		tokenModel := codexRealtimeModel(modelFromJSON(tokenSession))
		if selectionModel != tokenModel {
			writeRealtimeError(c, http.StatusForbidden, "Realtime client secret is not valid for the requested model", "invalid_request_error", "realtime_client_secret_scope_mismatch")
			return
		}
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	ctx = coreexecutor.WithDownstreamWebsocket(ctx)
	selectionOpts := coreexecutor.Options{Headers: liveSelectionHeaders(c)}
	ctx = handlers.EnrichContextWithSessionHierarchy(ctx, selectionOpts.Headers, nil, nil)
	selection, selected, errSelect := h.selectOAuth(ctx, selectionModel, selectionOpts)
	if errSelect != nil {
		writeSelectionError(c, errSelect)
		return
	}
	if selected == nil {
		if selection != nil {
			selection.End("missing_auth")
		}
		writeRealtimeError(c, http.StatusServiceUnavailable, "Codex auth unavailable", "server_error", "codex_auth_unavailable")
		return
	}
	if selection != nil && selection.CanonicalSessionID != "" {
		meta := logging.GetClientRequestMetadata(ctx)
		meta.SessionID = selection.CanonicalSessionID
		if selection.ParentSessionID != "" {
			meta.ParentSessionID = selection.ParentSessionID
		} else {
			meta.ParentSessionID = ""
		}
		if meta.SessionID == meta.ParentSessionID {
			meta.ParentSessionID = ""
		}
		ctx = logging.WithClientRequestMetadata(ctx, meta)
	}
	if selection != nil {
		attemptCtx, releaseAttempt, errAttempt := selection.AttemptContext(ctx)
		if errAttempt != nil {
			selection.End("attempt_bind_failed")
			writeRealtimeError(c, http.StatusServiceUnavailable, errAttempt.Error(), "server_error", "realtime_upstream_unavailable")
			return
		}
		ctx = attemptCtx
		defer releaseAttempt()
		selection.Retain()
		defer selection.End("session_closed")
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())

	upstreamURL := h.directRealtimeURL(requestedModel)
	dialUpstream := func(current *auth.Auth) (*websocket.Conn, *http.Response, error) {
		request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, websocketHTTPURL(upstreamURL), nil)
		if errRequest != nil {
			return nil, nil, errRequest
		}
		request.Header = directRealtimeHeaders(c.Request.Header)
		setAccountHeader(request.Header, current)
		if errPrepare := h.authManager.PrepareHttpRequest(ctx, current, request); errPrepare != nil {
			return nil, nil, errPrepare
		}
		authType, authValue := current.AccountInfo()
		helpersConfig := h.currentConfig()
		helps.RecordAPIWebsocketRequest(ctx, helpersConfig, helps.UpstreamRequestLog{
			URL:       upstreamURL,
			Method:    "WEBSOCKET",
			Headers:   headersForLogging(request.Header),
			Provider:  "codex",
			AuthID:    current.ID,
			AuthLabel: current.Label,
			AuthType:  authType,
			AuthValue: authValue,
		})
		dialer := newProxyAwareSidebandDialer(helpersConfig, current)
		dialer.Subprotocols = websocket.Subprotocols(c.Request)
		return dialer.DialContext(ctx, upstreamURL, request.Header)
	}

	upstream, handshakeResponse, errDial := dialUpstream(selected)
	if errDial != nil {
		status := clienterror.HTTPStatusFromErrorOr(errDial, http.StatusBadGateway)
		helpConfig := h.currentConfig()
		var responseBody []byte
		if handshakeResponse != nil && handshakeResponse.StatusCode > 0 {
			status = handshakeResponse.StatusCode
			copyRealtimeHandshakeHeaders(c.Writer.Header(), handshakeResponse.Header)
			helps.RecordAPIWebsocketHandshake(ctx, helpConfig, handshakeResponse.StatusCode, callResponseHeaders(handshakeResponse.Header))
			if handshakeResponse.Body != nil {
				var errRead error
				responseBody, errRead = readLimitedBody(handshakeResponse.Body)
				if errRead != nil {
					log.Errorf("codex realtime: read rejected handshake body error: %v", errRead)
				}
				helps.AppendAPIWebsocketResponse(ctx, helpConfig, responseBody)
			}
		}
		closeHandshakeBody(handshakeResponse, "direct websocket rejected")
		if selection != nil && status == http.StatusUnauthorized {
			diagnosticBody := responseBody
			if len(diagnosticBody) == 0 {
				diagnosticBody = []byte(errDial.Error())
			}
			h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, diagnosticBody)
			log.WithField("status", status).Warnf("codex realtime websocket upstream handshake failed: %s", logging.SafeDiagnosticForLog(string(diagnosticBody)))
		}
		helps.RecordAPIWebsocketError(ctx, helpConfig, "dial", errDial)
		if handshakeResponse != nil && handshakeResponse.StatusCode == http.StatusUnauthorized {
			if contentType := handshakeResponse.Header.Get("Content-Type"); contentType != "" {
				c.Header("Content-Type", contentType)
			}
			c.Status(handshakeResponse.StatusCode)
			if len(responseBody) > 0 {
				if _, errWrite := c.Writer.Write(responseBody); errWrite != nil {
					log.WithError(errWrite).Warn("codex realtime: write rejected handshake body failed")
				}
			}
			return
		}
		helpDetails := "Codex Realtime WebSocket upstream unavailable"
		helpType := "api_error"
		if status == http.StatusNotFound || status == http.StatusNotImplemented {
			helpDetails = "Direct Realtime WebSocket is not supported by the Codex OAuth upstream"
			helpType = "not_supported_error"
			status = http.StatusNotImplemented
		}
		helpCode := "realtime_websocket_upstream_unavailable"
		if helpType == "not_supported_error" {
			helpCode = "realtime_capability_not_supported"
		} else if status == http.StatusUnauthorized {
			helpType = "authentication_error"
			helpCode = "realtime_upstream_unauthorized"
		}
		writeRealtimeError(c, status, helpDetails, helpType, helpCode)
		return
	}
	closeHandshakeBody(handshakeResponse, "direct websocket handshake")
	closeUpstream := websocketCloseFunc("upstream", upstream)
	defer func() { _ = closeUpstream() }()
	if len(tokenSession) > 0 {
		updateSession, errSession := realtimeSessionUpdate(tokenSession)
		if errSession != nil {
			_ = closeUpstream()
			writeRealtimeError(c, http.StatusInternalServerError, "Failed to apply Realtime client secret session", "server_error", "realtime_session_failed")
			return
		}
		update, errMarshal := json.Marshal(struct {
			Type    string          `json:"type"`
			Session json.RawMessage `json:"session"`
		}{Type: "session.update", Session: updateSession})
		if errMarshal != nil {
			_ = closeUpstream()
			writeRealtimeError(c, http.StatusInternalServerError, "Failed to apply Realtime client secret session", "server_error", "realtime_session_failed")
			return
		}
		if errWrite := upstream.WriteMessage(websocket.TextMessage, update); errWrite != nil {
			_ = closeUpstream()
			writeRealtimeError(c, http.StatusBadGateway, "Failed to apply Realtime client secret session", "api_error", "realtime_upstream_unavailable")
			return
		}
	}

	if selection != nil {
		if errBind := selection.Bind(closeUpstream); errBind != nil {
			writeRealtimeError(c, http.StatusServiceUnavailable, errBind.Error(), "server_error", "realtime_upstream_unavailable")
			return
		}
	}

	upgradeHeaders := make(http.Header)
	if subprotocol := upstream.Subprotocol(); subprotocol != "" {
		upgradeHeaders.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	downstream, errUpgrade := sidebandUpgrader.Upgrade(c.Writer, c.Request, upgradeHeaders)
	if errUpgrade != nil {
		_ = closeUpstream()
		return
	}
	closeDownstream := websocketCloseFunc("downstream", downstream)
	defer func() { _ = closeDownstream() }()
	if selection != nil {
		if errBind := selection.Bind(closeDownstream); errBind != nil {
			return
		}
	}

	if errRelay := relayWebsockets(downstream, upstream); errRelay != nil && !isNormalWebsocketClose(errRelay) {
		helps.RecordAPIWebsocketError(ctx, h.currentConfig(), "relay", errRelay)
		log.WithError(errRelay).Debug("codex realtime direct websocket relay closed")
	}
}

func realtimeSessionUpdate(session json.RawMessage) (json.RawMessage, error) {
	var update map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(session, &update); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	for _, field := range []string{"model", "id", "object", "expires_at", "client_secret"} {
		delete(update, field)
	}
	return json.Marshal(update)
}

func (h *Handler) directRealtimeURL(model string) string {
	values := make(url.Values)
	values.Set("model", strings.TrimSpace(model))
	return strings.TrimRight(h.sidebandAPIBaseURL, "/") + "/realtime?" + values.Encode()
}

func directRealtimeHeaders(source http.Header) http.Header {
	headers := protocolHeaders(source)
	headers.Del("OpenAI-Alpha")
	if headers.Get("Originator") == "" {
		headers.Set("Originator", "Codex Desktop")
	}
	return headers
}

func copyRealtimeHandshakeHeaders(destination, source http.Header) {
	for _, name := range []string{"Retry-After", "X-Request-Id", "OpenAI-Request-Id"} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

func closeHandshakeBody(response *http.Response, label string) {
	if response == nil || response.Body == nil {
		return
	}
	if errClose := response.Body.Close(); errClose != nil {
		log.Errorf("codex realtime: close %s response body error: %v", label, errClose)
	}
}
