// Package live forwards Codex realtime WebRTC session bootstrap requests.
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	upstreamCallURL  = "https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas"
	defaultLiveModel = "gpt-live-1-codex"
	maxBodySize      = 16 << 20
)

var liveProtocolHeaders = []string{
	"OpenAI-Alpha",
	"X-Session-Id",
	"Session-Id",
	"Thread-Id",
	"Originator",
	"OpenAI-Safety-Identifier",
	"OpenAI-Organization",
	"OpenAI-Project",
	"X-Oai-Attestation",
}

// Handler forwards Codex live session requests through the shared auth scheduler.
type Handler struct {
	authManager          *auth.Manager
	cfg                  *config.Config
	sessions             *sessionStore
	clientSecrets        *clientSecretStore
	sidebandAPIBaseURL   string
	mediaRelayMu         sync.RWMutex
	mediaRelay           mediaRelayFactory
	mediaRelayErr        error
	mediaRelayConfig     config.CodexLiveMediaRelayConfig
	mediaRelayConfigured bool
	mediaLimiter         *mediaSessionLimiter
}

// NewHandler creates a Codex live session handler.
func NewHandler(authManager *auth.Manager, cfg *config.Config) *Handler {
	handler := &Handler{
		authManager:        authManager,
		cfg:                cfg,
		sessions:           newSessionStore(),
		clientSecrets:      newClientSecretStore(),
		sidebandAPIBaseURL: defaultSidebandAPIBaseURL,
	}
	if errUpdate := handler.UpdateConfig(cfg); errUpdate != nil {
		log.WithError(errUpdate).Error("failed to configure Codex Live media relay")
	}
	return handler
}

// UpdateConfig atomically applies Codex Live media relay settings to new sessions.
func (h *Handler) UpdateConfig(cfg *config.Config) error {
	if h == nil {
		return nil
	}
	var relayConfig config.CodexLiveMediaRelayConfig
	if cfg != nil {
		relayConfig = cfg.Codex.LiveMediaRelay
	}
	h.mediaRelayMu.Lock()
	previousConfig := h.mediaRelayConfig
	previouslyConfigured := h.mediaRelayConfigured
	h.cfg = cfg
	if previouslyConfigured && reflect.DeepEqual(previousConfig, relayConfig) {
		currentErr := h.mediaRelayErr
		h.mediaRelayMu.Unlock()
		return currentErr
	}
	if h.mediaLimiter == nil {
		h.mediaLimiter = &mediaSessionLimiter{}
	}
	var relay mediaRelayFactory
	var relayErr error
	if relayConfig.Enabled {
		relay, relayErr = newPionMediaRelayWithLimiter(relayConfig, h.mediaLimiter)
	}
	h.mediaRelay = relay
	h.mediaRelayErr = relayErr
	h.mediaRelayConfig = relayConfig
	h.mediaRelayConfigured = true
	h.mediaRelayMu.Unlock()

	if relayErr == nil && (previouslyConfigured || relayConfig.Enabled) {
		message := "codex live media relay configured"
		if previouslyConfigured {
			message = "codex live media relay configuration reloaded; changes apply to new sessions"
		}
		log.WithFields(liveMediaConfigLogFields(relayConfig)).Info(message)
	}
	return relayErr
}

func liveMediaConfigLogFields(relayConfig config.CodexLiveMediaRelayConfig) log.Fields {
	publicIP := strings.TrimSpace(relayConfig.PublicIP)
	if publicIP == "" {
		publicIP = "auto"
	}
	return log.Fields{
		"enabled":                    relayConfig.Enabled,
		"max_sessions":               relayConfig.EffectiveMaxSessions(),
		"disable_private_remote_ips": relayConfig.DisablePrivateRemoteIPs,
		"public_ip":                  publicIP,
		"udp_port_min":               relayConfig.UDPPortMin,
		"udp_port_max":               relayConfig.UDPPortMax,
		"ice_server_count":           len(relayConfig.ICEServers),
	}
}

func (h *Handler) currentRuntime() (*config.Config, mediaRelayFactory, error) {
	if h == nil {
		return nil, nil, nil
	}
	h.mediaRelayMu.RLock()
	cfg := h.cfg
	relay := h.mediaRelay
	relayErr := h.mediaRelayErr
	h.mediaRelayMu.RUnlock()
	return cfg, relay, relayErr
}

func (h *Handler) currentConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mediaRelayMu.RLock()
	cfg := h.cfg
	h.mediaRelayMu.RUnlock()
	return cfg
}

func (h *Handler) currentMediaRelay() (mediaRelayFactory, error) {
	if h == nil {
		return nil, nil
	}
	h.mediaRelayMu.RLock()
	relay := h.mediaRelay
	relayErr := h.mediaRelayErr
	h.mediaRelayMu.RUnlock()
	return relay, relayErr
}

// Close releases all active Codex live sessions.
func (h *Handler) Close() {
	if h == nil {
		return
	}
	if h.sessions != nil {
		h.sessions.closeAll("server_stopped")
	}
	if h.clientSecrets != nil {
		h.clientSecrets.close()
	}
}

// Handle forwards a WebRTC SDP bootstrap request to the Codex realtime calls endpoint.
func (h *Handler) Handle(c *gin.Context) {
	if h == nil || h.authManager == nil {
		writeLiveError(c, http.StatusServiceUnavailable, "Codex auth manager unavailable")
		return
	}

	body, errRead := readBody(c.Request.Body)
	if errRead != nil {
		status := clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest)
		if errors.Is(errRead, errBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeLiveError(c, status, errRead.Error())
		return
	}
	upstreamBody, upstreamContentType, model, errPayload := prepareCallRequest(body, c.GetHeader("Content-Type"))
	if errPayload == nil {
		upstreamBody, upstreamContentType, model, errPayload = applyClientSecretCallSession(upstreamBody, upstreamContentType, model, clientSecretSession(c))
	}
	if errPayload == nil {
		upstreamBody, model, errPayload = rewriteCallRequestModel(upstreamBody, upstreamContentType, model)
	}
	if errPayload != nil {
		writeLiveError(c, http.StatusBadRequest, errPayload.Error())
		return
	}
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
		OriginalRequest: body,
	}
	selection, selected, errSelect := h.selectOAuth(ctx, model, selectionOpts)
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

	if mediaRelay != nil {
		clientOffer, errSDP := callRequestSDP(upstreamBody, upstreamContentType)
		if errSDP != nil {
			writeLiveError(c, http.StatusBadRequest, errSDP.Error())
			return
		}
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
					log.WithError(errClose).Debug("codex live media: close unretained session")
				}
			}
		}()
		upstreamBody, upstreamContentType, errSDP = replaceCallRequestSDP(upstreamBody, upstreamContentType, upstreamOffer)
		if errSDP != nil {
			writeLiveError(c, http.StatusBadRequest, errSDP.Error())
			return
		}
	}

	baseHeaders := protocolHeaders(c.Request.Header)
	baseHeaders.Set("Content-Type", upstreamContentType)
	performRequest := func(current *auth.Auth) (*http.Response, error) {
		headers := baseHeaders.Clone()
		setAccountHeader(headers, current)
		req, errRequest := h.authManager.NewHttpRequest(ctx, current, http.MethodPost, upstreamCallURL, upstreamBody, headers)
		if errRequest != nil {
			return nil, errRequest
		}
		authType, authValue := current.AccountInfo()
		helps.RecordAPIRequest(ctx, runtimeConfig, helps.UpstreamRequestLog{
			URL:       upstreamCallURL,
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
				log.Errorf("codex live: close response body error: %v", closeResponseErr)
			}
		})
		return closeResponseErr
	}
	defer func() { _ = closeResponseBody() }()
	if selection != nil {
		if errBind := selection.Bind(closeResponseBody); errBind != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", model)
			}
			selection.End("response_bind_failed")
			writeLiveError(c, http.StatusServiceUnavailable, errBind.Error())
			return
		}
	}

	responseHeaders := callResponseHeaders(resp.Header)
	helps.RecordAPIResponseMetadata(ctx, runtimeConfig, resp.StatusCode, responseHeaders)
	responseBody, errResponse := readLimitedBody(resp.Body)
	if errResponse != nil {
		helps.AppendAPIResponseChunk(ctx, runtimeConfig, responseBody)
		if selection != nil && resp.StatusCode == http.StatusUnauthorized {
			h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", model, responseBody)
		}
		helps.RecordAPIResponseError(ctx, runtimeConfig, errResponse)
		message := "Failed to read Codex live response"
		status := clienterror.HTTPStatusFromErrorOr(errResponse, http.StatusBadGateway)
		if errors.Is(errResponse, errBodyTooLarge) {
			message = "Codex live response body too large"
			status = http.StatusBadGateway
		}
		writeLiveError(c, status, message)
		return
	}
	helps.AppendAPIResponseChunk(ctx, runtimeConfig, responseBody)
	if selection != nil && resp.StatusCode == http.StatusUnauthorized {
		h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", model, responseBody)
		log.WithField("status", resp.StatusCode).Warnf("codex live upstream request failed: %s", logging.SafeDiagnosticForLog(string(responseBody)))
	}
	responseBodyToWrite := responseBody
	success := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	callID := ""
	if success {
		callID = callIDFromLocation(resp.Header.Get("Location"))
		if callID == "" && mediaSession != nil {
			writeLiveError(c, http.StatusBadGateway, "Codex live response is missing a valid call ID")
			return
		}
		if mediaSession != nil {
			mediaSession.SetCallID(callID)
		}
		if callID != "" && strings.HasPrefix(c.Request.URL.Path, "/v1/realtime") {
			responseHeaders.Set("Location", "/v1/realtime/calls/"+callID)
		}
	}
	if success && mediaSession != nil {
		upstreamAnswer, errSDP := callResponseSDP(responseBody, resp.Header.Get("Content-Type"))
		if errSDP != nil {
			writeLiveError(c, http.StatusBadGateway, errSDP.Error())
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
	var storedSession liveSession
	sessionStored := false
	if success && h.sessions != nil {
		if callID != "" {
			session := liveSession{authID: selected.ID, model: model, media: mediaSession}
			session.ownerPrincipal, session.ownerProvider = requestOwner(c)
			if principal, ok := c.Get(ClientSecretPrincipalContextKey); ok {
				session.clientSecretPrincipal, _ = principal.(string)
			}
			if selection != nil {
				if mediaSession != nil {
					if errBind := selection.Bind(func() error {
						return mediaSession.CloseWithReason("home_selection_closed")
					}); errBind != nil {
						selection.End("media_bind_failed")
						writeLiveError(c, http.StatusServiceUnavailable, errBind.Error())
						return
					}
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
			storedSession = h.sessions.put(callID, session)
			sessionStored = storedSession.callID != ""
			if mediaSession != nil {
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
		if sessionStored {
			h.sessions.complete(storedSession, "response_write_failed")
		}
		helps.RecordAPIResponseError(ctx, runtimeConfig, errWrite)
		log.WithError(errWrite).Warn("codex live: write response body failed")
	}
}

func mediaCredentialName(selected *auth.Auth, authIndex string) string {
	if selected == nil {
		return strings.TrimSpace(authIndex)
	}
	if label := strings.TrimSpace(selected.Label); label != "" {
		return label
	}
	if fileName := strings.TrimSpace(selected.FileName); fileName != "" {
		if baseName := strings.TrimSpace(filepath.Base(fileName)); baseName != "" && baseName != "." {
			return baseName
		}
	}
	return strings.TrimSpace(authIndex)
}

func (h *Handler) selectOAuth(ctx context.Context, model string, opts coreexecutor.Options) (*auth.HomeDispatchSelection, *auth.Auth, error) {
	var selection *auth.HomeDispatchSelection
	var selected *auth.Auth
	var errSelect error
	if h.authManager.HomeEnabled() {
		selection, errSelect = h.authManager.SelectHomeAuthByKind(ctx, "codex", model, auth.AuthKindOAuth, opts)
		if selection != nil {
			selected = selection.CloneAuth()
		}
	} else {
		selected, errSelect = h.authManager.SelectAuthByKind(ctx, "codex", "", auth.AuthKindOAuth, opts)
	}
	if errSelect != nil && selection != nil {
		selection.End("selection_failed")
	}
	return selection, selected, errSelect
}

var errBodyTooLarge = errors.New("Codex live request body too large")

func readBody(body io.Reader) ([]byte, error) {
	payload, errRead := readLimitedBody(body)
	if errRead != nil {
		if errors.Is(errRead, errBodyTooLarge) {
			return nil, errRead
		}
		return nil, fmt.Errorf("failed to read Codex live request: %w", errRead)
	}
	return payload, nil
}

func readLimitedBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	payload, errRead := io.ReadAll(io.LimitReader(body, maxBodySize+1))
	if errRead != nil {
		if len(payload) > maxBodySize {
			payload = payload[:maxBodySize]
		}
		return payload, errRead
	}
	if len(payload) > maxBodySize {
		return payload[:maxBodySize], errBodyTooLarge
	}
	return payload, nil
}

func prepareCallRequest(body []byte, contentType string) ([]byte, string, string, error) {
	mediaType, params, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		return multipartCallRequest(body, strings.TrimSpace(params["boundary"]))
	}
	model := modelFromJSON(body)
	if model == "" {
		model = defaultLiveModel
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	return body, contentType, model, nil
}

func applyClientSecretCallSession(body []byte, contentType, model string, session json.RawMessage) ([]byte, string, string, error) {
	if len(session) == 0 {
		return body, contentType, model, nil
	}
	mediaType, _, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType == nil && (strings.EqualFold(mediaType, "application/sdp") || strings.EqualFold(mediaType, "text/plain")) {
		encoded, errEncode := encodeCallRequest(string(body), session)
		if errEncode != nil {
			return nil, "", "", errEncode
		}
		return encoded, "application/json", modelFromJSON(session), nil
	}
	if errMediaType != nil || !strings.EqualFold(mediaType, "application/json") {
		return nil, "", "", errors.New("Realtime client secrets require an SDP or JSON call request")
	}
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, "", "", fmt.Errorf("failed to decode Realtime call request: %w", errUnmarshal)
	}
	payload["session"] = append(json.RawMessage(nil), session...)
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, "", "", fmt.Errorf("failed to encode Realtime call request: %w", errMarshal)
	}
	return encoded, "application/json", modelFromJSON(session), nil
}

func rewriteCallRequestModel(body []byte, contentType, model string) ([]byte, string, error) {
	upstreamModel := codexRealtimeModel(model)
	mediaType, _, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType != nil || !strings.EqualFold(mediaType, "application/json") || len(bytes.TrimSpace(body)) == 0 {
		return body, upstreamModel, nil
	}
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, "", fmt.Errorf("failed to decode Realtime call request: %w", errUnmarshal)
	}
	changed := false
	if sessionJSON, ok := payload["session"]; ok && len(sessionJSON) > 0 {
		var session map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(sessionJSON, &session); errUnmarshal != nil {
			return nil, "", fmt.Errorf("failed to decode Realtime session: %w", errUnmarshal)
		}
		encodedModel, errMarshal := json.Marshal(upstreamModel)
		if errMarshal != nil {
			return nil, "", fmt.Errorf("failed to encode Realtime model: %w", errMarshal)
		}
		session["model"] = encodedModel
		encodedSession, errMarshal := json.Marshal(session)
		if errMarshal != nil {
			return nil, "", fmt.Errorf("failed to encode Realtime session: %w", errMarshal)
		}
		payload["session"] = encodedSession
		changed = true
	} else if _, ok := payload["model"]; ok {
		encodedModel, errMarshal := json.Marshal(upstreamModel)
		if errMarshal != nil {
			return nil, "", fmt.Errorf("failed to encode Realtime model: %w", errMarshal)
		}
		payload["model"] = encodedModel
		changed = true
	}
	if !changed {
		return body, upstreamModel, nil
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, "", fmt.Errorf("failed to encode Realtime call request: %w", errMarshal)
	}
	return encoded, upstreamModel, nil
}

func multipartCallRequest(body []byte, boundary string) ([]byte, string, string, error) {
	if boundary == "" {
		return nil, "", "", errors.New("Codex live multipart boundary is missing")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var sdp *string
	var session json.RawMessage
	model := ""
	for {
		part, errPart := reader.NextPart()
		if errors.Is(errPart, io.EOF) {
			break
		}
		if errPart != nil {
			return nil, "", "", fmt.Errorf("failed to parse Codex live multipart body: %w", errPart)
		}
		partBody, errRead := io.ReadAll(part)
		errClose := part.Close()
		if errRead != nil {
			return nil, "", "", fmt.Errorf("failed to read Codex live multipart field: %w", errRead)
		}
		if errClose != nil {
			return nil, "", "", fmt.Errorf("failed to close Codex live multipart field: %w", errClose)
		}

		switch part.FormName() {
		case "sdp":
			value := string(partBody)
			sdp = &value
		case "session":
			if !json.Valid(partBody) {
				return nil, "", "", errors.New("Codex live session field must contain valid JSON")
			}
			session = append(json.RawMessage(nil), partBody...)
			model = modelFromJSON(partBody)
		}
	}
	if sdp == nil {
		return nil, "", "", errors.New("Codex live multipart body requires an sdp field")
	}
	if model == "" {
		model = defaultLiveModel
	}

	encoded, errEncode := encodeCallRequest(*sdp, session)
	if errEncode != nil {
		return nil, "", "", errEncode
	}
	return encoded, "application/json", model, nil
}

func encodeCallRequest(sdp string, session json.RawMessage) ([]byte, error) {
	payload := struct {
		SDP     string          `json:"sdp"`
		Session json.RawMessage `json:"session,omitempty"`
	}{
		SDP:     sdp,
		Session: session,
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("failed to encode Codex live request: %w", errMarshal)
	}
	return encoded, nil
}

func callRequestSDP(body []byte, contentType string) (string, error) {
	mediaType, _, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType == nil && (strings.EqualFold(mediaType, "application/sdp") || strings.EqualFold(mediaType, "text/plain")) {
		if strings.TrimSpace(string(body)) == "" {
			return "", errors.New("Codex live call request requires an SDP offer")
		}
		return string(body), nil
	}
	if errMediaType != nil || !strings.EqualFold(mediaType, "application/json") {
		return "", errors.New("Codex live media relay requires an SDP or JSON call request")
	}
	var payload struct {
		SDP string `json:"sdp"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return "", fmt.Errorf("failed to decode Codex live call request: %w", errUnmarshal)
	}
	if strings.TrimSpace(payload.SDP) == "" {
		return "", errors.New("Codex live call request requires an SDP offer")
	}
	return payload.SDP, nil
}

func replaceCallRequestSDP(body []byte, contentType, sdp string) ([]byte, string, error) {
	mediaType, _, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType == nil && (strings.EqualFold(mediaType, "application/sdp") || strings.EqualFold(mediaType, "text/plain")) {
		encoded, errEncode := encodeCallRequest(sdp, nil)
		if errEncode != nil {
			return nil, "", errEncode
		}
		return encoded, "application/json", nil
	}
	if errMediaType != nil || !strings.EqualFold(mediaType, "application/json") {
		return nil, "", errors.New("Codex live media relay requires an SDP or JSON call request")
	}
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, "", fmt.Errorf("failed to decode Codex live call request: %w", errUnmarshal)
	}
	encodedSDP, errMarshal := json.Marshal(sdp)
	if errMarshal != nil {
		return nil, "", fmt.Errorf("failed to encode Codex live SDP offer: %w", errMarshal)
	}
	payload["sdp"] = encodedSDP
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, "", fmt.Errorf("failed to encode Codex live call request: %w", errMarshal)
	}
	return encoded, "application/json", nil
}

func callResponseSDP(body []byte, contentType string) (string, error) {
	mediaType, _, errMediaType := mime.ParseMediaType(contentType)
	if errMediaType == nil && strings.EqualFold(mediaType, "application/json") {
		var payload struct {
			SDP string `json:"sdp"`
		}
		if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
			return "", fmt.Errorf("failed to decode Codex live response: %w", errUnmarshal)
		}
		if strings.TrimSpace(payload.SDP) == "" {
			return "", errors.New("Codex live response requires an SDP answer")
		}
		return payload.SDP, nil
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", errors.New("Codex live response requires an SDP answer")
	}
	return string(body), nil
}

func modelFromJSON(body []byte) string {
	var payload struct {
		Model   string `json:"model"`
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ""
	}
	if model := strings.TrimSpace(payload.Session.Model); model != "" {
		return model
	}
	return strings.TrimSpace(payload.Model)
}

func protocolHeaders(source http.Header) http.Header {
	headers := make(http.Header)
	for _, name := range liveProtocolHeaders {
		for _, value := range source.Values(name) {
			headers.Add(name, value)
		}
	}
	return headers
}

func setAccountHeader(headers http.Header, selected *auth.Auth) {
	if selected == nil {
		return
	}
	if accountID, ok := selected.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
}

func headersForLogging(source http.Header) http.Header {
	headers := source.Clone()
	if headers.Get("X-Oai-Attestation") != "" {
		headers.Set("X-Oai-Attestation", "[REDACTED]")
	}
	return headers
}

func callResponseHeaders(source http.Header) http.Header {
	headers := make(http.Header)
	for _, name := range []string{"Content-Type", "Location", "Retry-After", "X-Request-Id", "OpenAI-Request-Id"} {
		for _, value := range source.Values(name) {
			headers.Add(name, value)
		}
	}
	return headers
}

func writeResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func writeLiveError(c *gin.Context, status int, message string) {
	if c != nil && c.Request != nil && c.Request.URL != nil && strings.HasPrefix(c.Request.URL.Path, "/v1/realtime") {
		errorType := "api_error"
		if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
			errorType = "invalid_request_error"
		}
		if status == http.StatusUnauthorized {
			errorType = "authentication_error"
		}
		writeRealtimeError(c, status, message, errorType, "realtime_request_failed")
		return
	}
	c.JSON(status, gin.H{"error": message})
}

func writeSelectionError(c *gin.Context, err error) {
	status := clienterror.HTTPStatusFromErrorOr(err, http.StatusServiceUnavailable)
	for _, value := range auth.SafeResponseHeaders(err).Values("Retry-After") {
		c.Writer.Header().Add("Retry-After", value)
	}
	writeLiveError(c, status, err.Error())
}
