package live

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// handleStandardRealtimeCall exchanges a WebRTC SDP offer with the standard
// OpenAI Realtime endpoint, preserving the requested GPT Realtime model instead
// of substituting the Codex Live (Quicksilver) model. Credential selection still
// uses the Codex-mapped model so the scheduler picks a realtime-capable OAuth
// credential, mirroring the standard Realtime WebSocket path.
func (h *Handler) handleStandardRealtimeCall(c *gin.Context, originalBody []byte, body []byte, contentType, model string) {
	if h == nil || h.authManager == nil {
		writeLiveError(c, http.StatusServiceUnavailable, "Codex auth manager unavailable")
		return
	}
	clientOffer, errSDP := callRequestSDP(body, contentType)
	if errSDP != nil {
		writeLiveError(c, http.StatusBadRequest, errSDP.Error())
		return
	}
	sessionConfig := callRequestSession(body, contentType)
	runtimeConfig, mediaRelay, mediaRelayErr := h.currentRuntime()
	if mediaRelayErr != nil {
		writeLiveError(c, http.StatusServiceUnavailable, mediaRelayErr.Error())
		return
	}
	var mediaSession mediaRelaySession
	mediaRetained := false

	ctx := context.WithValue(c.Request.Context(), "gin", c)
	selectionOpts := coreexecutor.Options{
		Headers:         liveSelectionHeaders(c),
		OriginalRequest: originalBody,
	}
	ctx = handlers.EnrichContextWithSessionHierarchy(ctx, selectionOpts.Headers, originalBody, nil)
	selectionModel := codexRealtimeModel(model)
	selection, selected, errSelect := h.selectOAuth(ctx, selectionModel, selectionOpts)
	if errSelect != nil {
		writeSelectionError(c, errSelect)
		return
	}
	if selected == nil {
		if selection != nil {
			selection.End("missing_auth")
		}
		writeLiveError(c, http.StatusServiceUnavailable, "Codex auth unavailable")
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
			writeLiveError(c, http.StatusServiceUnavailable, errAttempt.Error())
			return
		}
		ctx = attemptCtx
		defer releaseAttempt()
	}
	selectedIndex := selected.EnsureIndex()
	logging.SetGinCPATraceID(c, selectedIndex)
	if selection != nil {
		defer func() {
			if selection.Active() && !selection.Retained() {
				selection.End("request_closed")
			}
		}()
	}

	offerToSend := clientOffer
	if mediaRelay != nil {
		var upstreamOffer string
		mediaSession, upstreamOffer, errSDP = mediaRelay.NewSession(ctx, clientOffer, mediaSessionRoute{
			proxyURL:   proxyURLForAuth(runtimeConfig, selected),
			credential: mediaCredentialName(selected, selectedIndex),
			authIndex:  selectedIndex,
		})
		if errSDP != nil {
			writeLiveError(c, clienterror.HTTPStatusFromErrorOr(errSDP, http.StatusBadGateway), errSDP.Error())
			return
		}
		defer func() {
			if !mediaRetained {
				if errClose := mediaSession.CloseWithReason("request_not_retained"); errClose != nil {
					log.WithError(errClose).Debug("standard realtime media: close unretained session")
				}
			}
		}()
		if len(sessionConfig) > 0 {
			if errQueue := queueRealtimeSessionUpdate(mediaSession, sessionConfig); errQueue != nil {
				log.WithError(errQueue).Warn("standard realtime: queue session update on media relay failed")
			}
		}
		offerToSend = upstreamOffer
	}

	upstreamURL := h.standardRealtimeCallURL(model)
	baseHeaders := directRealtimeHeaders(c.Request.Header)
	baseHeaders.Set("Content-Type", "application/sdp")
	upstreamBody := []byte(offerToSend)
	performRequest := func(current *auth.Auth) (*http.Response, error) {
		headers := baseHeaders.Clone()
		setAccountHeader(headers, current)
		req, errRequest := h.authManager.NewHttpRequest(ctx, current, http.MethodPost, upstreamURL, upstreamBody, headers)
		if errRequest != nil {
			return nil, errRequest
		}
		authType, authValue := current.AccountInfo()
		helps.RecordAPIRequest(ctx, runtimeConfig, helps.UpstreamRequestLog{
			URL:       upstreamURL,
			Method:    http.MethodPost,
			Headers:   headersForLogging(req.Header),
			Body:      upstreamBody,
			Provider:  "codex",
			AuthID:    current.ID,
			AuthLabel: current.Label,
			AuthType:  authType,
			AuthValue: authValue,
		})
		return h.authManager.HttpRequest(ctx, current, req)
	}

	if errContext := ctx.Err(); errContext != nil {
		if selection != nil {
			selection.End("attempt_canceled")
		}
		writeLiveError(c, clienterror.HTTPStatusFromErrorOr(errContext, http.StatusRequestTimeout), errContext.Error())
		return
	}
	resp, errRequest := performRequest(selected)
	if errRequest != nil {
		if selection != nil {
			selection.End("request_failed")
		}
		helps.RecordAPIResponseError(ctx, runtimeConfig, errRequest)
		writeLiveError(c, clienterror.HTTPStatusFromErrorOr(errRequest, http.StatusBadGateway), errRequest.Error())
		return
	}
	var closeResponseOnce sync.Once
	var closeResponseErr error
	closeResponseBody := func() error {
		closeResponseOnce.Do(func() {
			closeResponseErr = resp.Body.Close()
			if closeResponseErr != nil {
				log.Errorf("standard realtime: close response body error: %v", closeResponseErr)
			}
		})
		return closeResponseErr
	}
	defer func() { _ = closeResponseBody() }()
	if selection != nil {
		if errBind := selection.Bind(closeResponseBody); errBind != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel)
			}
			selection.End("response_bind_failed")
			writeLiveError(c, http.StatusServiceUnavailable, errBind.Error())
			return
		}
	}

	responseHeaders := callResponseHeaders(resp.Header)
	responseHeaders.Del("Location")
	helps.RecordAPIResponseMetadata(ctx, runtimeConfig, resp.StatusCode, responseHeaders)
	responseBody, errResponse := readLimitedBody(resp.Body)
	if errResponse != nil {
		helps.AppendAPIResponseChunk(ctx, runtimeConfig, responseBody)
		if selection != nil && resp.StatusCode == http.StatusUnauthorized {
			h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, responseBody)
		}
		helps.RecordAPIResponseError(ctx, runtimeConfig, errResponse)
		message := "Failed to read Realtime WebRTC response"
		status := clienterror.HTTPStatusFromErrorOr(errResponse, http.StatusBadGateway)
		if errors.Is(errResponse, errBodyTooLarge) {
			message = "Realtime WebRTC response body too large"
			status = http.StatusBadGateway
		}
		writeLiveError(c, status, message)
		return
	}
	helps.AppendAPIResponseChunk(ctx, runtimeConfig, responseBody)
	if selection != nil && resp.StatusCode == http.StatusUnauthorized {
		h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, responseBody)
		log.WithField("status", resp.StatusCode).Warnf("standard realtime upstream request failed: %s", logging.SafeDiagnosticForLog(string(responseBody)))
	}
	responseBodyToWrite := responseBody
	success := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	if success && mediaSession != nil {
		upstreamAnswer, errAnswerSDP := callResponseSDP(responseBody, resp.Header.Get("Content-Type"))
		if errAnswerSDP != nil {
			writeLiveError(c, http.StatusBadGateway, errAnswerSDP.Error())
			return
		}
		downstreamAnswer, errAnswer := mediaSession.AcceptUpstreamAnswer(ctx, upstreamAnswer)
		if errAnswer != nil {
			writeLiveError(c, clienterror.HTTPStatusFromErrorOr(errAnswer, http.StatusBadGateway), errAnswer.Error())
			return
		}
		responseBodyToWrite = []byte(downstreamAnswer)
		responseHeaders.Set("Content-Type", "application/sdp")
	}
	if success && mediaSession != nil && h.sessions != nil {
		callID, errID := randomRealtimeID("call_", 18)
		if errID != nil {
			log.WithError(errID).Warn("standard realtime: generate media session call ID failed")
		} else {
			mediaSession.SetCallID(callID)
			clientMeta := logging.GetClientRequestMetadata(ctx)
			session := liveSession{
				authID:          selected.ID,
				model:           model,
				media:           mediaSession,
				sessionID:       clientMeta.SessionID,
				parentSessionID: clientMeta.ParentSessionID,
			}
			session.ownerPrincipal, session.ownerProvider = requestOwner(c)
			if principal, ok := c.Get(ClientSecretPrincipalContextKey); ok {
				session.clientSecretPrincipal, _ = principal.(string)
			}
			if selection != nil {
				if errBind := selection.Bind(func() error {
					return mediaSession.CloseWithReason("home_selection_closed")
				}); errBind != nil {
					selection.End("media_bind_failed")
					writeLiveError(c, http.StatusServiceUnavailable, errBind.Error())
					return
				}
				if errBind := selection.Bind(func() error {
					// End outside the resource closer to avoid waiting on the closer itself.
					go selection.End("session_drained")
					return nil
				}); errBind != nil {
					selection.End("session_drain_bind_failed")
					writeLiveError(c, http.StatusServiceUnavailable, errBind.Error())
					return
				}
				selection.Retain()
				session.homeSelection = selection
			}
			storedSession := h.sessions.put(callID, session)
			if storedSession.callID != "" {
				mediaSession.SetCloseHandler(func(reason string) {
					h.sessions.complete(storedSession, reason)
				})
				mediaRetained = true
			}
		}
	}
	writeResponseHeaders(c.Writer.Header(), responseHeaders)
	c.Status(resp.StatusCode)
	if _, errWrite := c.Writer.Write(responseBodyToWrite); errWrite != nil {
		helps.RecordAPIResponseError(ctx, runtimeConfig, errWrite)
		log.WithError(errWrite).Warn("standard realtime: write response body failed")
	}
}

// standardRealtimeCallURL builds the standard OpenAI Realtime WebRTC SDP exchange
// endpoint URL for the requested model.
func (h *Handler) standardRealtimeCallURL(model string) string {
	values := make(url.Values)
	values.Set("model", strings.TrimSpace(model))
	return h.realtimeHTTPBaseURL() + "/realtime?" + values.Encode()
}

// callRequestSession extracts the embedded Realtime session configuration from a
// call request body, if present.
func callRequestSession(body []byte, contentType string) json.RawMessage {
	mediaType, _, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType != nil || !strings.EqualFold(mediaType, "application/json") {
		return nil
	}
	var payload struct {
		Session json.RawMessage `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil
	}
	return payload.Session
}

// queueRealtimeSessionUpdate queues a session.update event built from a Realtime
// session configuration on the relayed upstream DataChannel so the session
// configuration reaches the standard Realtime upstream, which only accepts SDP
// in the bootstrap request.
func queueRealtimeSessionUpdate(mediaSession mediaRelaySession, session json.RawMessage) error {
	update, errUpdate := realtimeSessionUpdate(session)
	if errUpdate != nil {
		return errUpdate
	}
	payload, errMarshal := json.Marshal(struct {
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
	}{Type: "session.update", Session: update})
	if errMarshal != nil {
		return errMarshal
	}
	return mediaSession.SendUpstreamText(string(payload))
}
