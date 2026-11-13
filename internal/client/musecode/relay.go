// Package musecode serves Muse Code CLI client endpoints, generating /muse-code/models
// from CPA's available models and relaying auxiliary endpoints to the selected Meta upstream.
package musecode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const maxBodySize = 16 << 20

var errBodyTooLarge = errors.New("muse code: body exceeds 16 MiB limit")

var allowedEndpoints = map[string]string{
	"/muse-code/config":       http.MethodGet,
	"/muse-code/search":       http.MethodPost,
	"/muse-code/browser_open": http.MethodPost,
}

var allowedRequestHeaders = []string{
	"Accept",
	"Content-Type",
	"Traceparent",
	"If-None-Match",
}

var allowedResponseHeaders = []string{
	"Content-Type",
	"Cache-Control",
	"Etag",
	"Last-Modified",
	"Retry-After",
}

// Handler serves Muse Code CLI endpoints, generating /muse-code/models locally
// and relaying allowlisted auxiliary endpoints to Meta using managed Meta credentials.
type Handler struct {
	mu               sync.RWMutex
	authManager      *auth.Manager
	cfg              *config.Config
	homeModelsLoader HomeModelsLoader
	modelListWriter  ModelListWriter
}

// NewHandler constructs a Muse Code endpoint relay handler.
func NewHandler(authManager *auth.Manager, cfg *config.Config) *Handler {
	return &Handler{
		authManager: authManager,
		cfg:         cfg,
	}
}

// UpdateConfig updates the handler's runtime configuration snapshot.
func (h *Handler) UpdateConfig(cfg *config.Config) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
}

func (h *Handler) currentConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	cfg := h.cfg
	h.mu.RUnlock()
	return cfg
}

type relayAttemptResponse struct {
	statusCode int
	headers    http.Header
	body       []byte
}

func writeRelayAttemptResponse(c *gin.Context, resp *relayAttemptResponse) {
	if c == nil || resp == nil {
		return
	}
	writeRelayResponseHeaders(c.Writer.Header(), resp.headers)
	c.Status(resp.statusCode)
	if len(resp.body) > 0 {
		_, _ = c.Writer.Write(resp.body)
	}
}

func isRequestScopedContinue(action string, okAction bool) bool {
	return okAction && (action == auth.RequestScopedActionContinue || action == auth.RequestScopedActionContinueAndCooldown)
}

// Handle serves GET /muse-code/models locally and forwards allowlisted /muse-code/*
// relay requests to the selected Meta origin.
func (h *Handler) Handle(c *gin.Context) {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return
	}
	if c.Request.Method == http.MethodGet && c.Request.URL.Path == "/muse-code/models" {
		h.HandleModels(c)
		return
	}
	if !isAllowedEndpoint(c.Request.Method, c.Request.URL.Path) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta auth manager unavailable"})
		return
	}
	if h.authManager.HomeEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Muse Code endpoints are unavailable while Home mode is enabled"})
		return
	}

	reqBody, errRead := readLimitedBody(c.Request.Body)
	if errRead != nil {
		if errors.Is(errRead, errBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": errRead.Error()})
			return
		}
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "Failed to read Muse Code request"})
		return
	}

	ctx := context.WithValue(c.Request.Context(), "gin", c)
	cfg := h.currentConfig()
	if cfg != nil && auth.RequestScopedErrorConfigFromContext(ctx) == nil {
		ctx = auth.WithRequestScopedErrorConfig(ctx, cfg)
	}
	selectionOpts := coreexecutor.Options{
		Headers:         c.Request.Header.Clone(),
		OriginalRequest: reqBody,
	}
	sanitizedQuery := sanitizeQuery(c.Request.URL.RawQuery)
	baseReqHeaders := relayRequestHeaders(c.Request.Header)
	maxRetryCreds := h.authManager.MaxRetryCredentials()
	tried := make(map[string]struct{})
	var lastResp *relayAttemptResponse
	var lastErr error

	for attempt := 0; maxRetryCreds <= 0 || attempt < maxRetryCreds; attempt++ {
		selected, errSelect := h.selectMetaAuthExcluding(ctx, selectionOpts, tried)
		if errSelect != nil {
			if lastResp != nil {
				writeRelayAttemptResponse(c, lastResp)
				return
			}
			if lastErr != nil {
				c.JSON(clienterror.HTTPStatusFromErrorOr(lastErr, http.StatusBadGateway), gin.H{"error": lastErr.Error()})
				return
			}
			status := clienterror.HTTPStatusFromErrorOr(errSelect, http.StatusServiceUnavailable)
			for _, value := range auth.SafeResponseHeaders(errSelect).Values("Retry-After") {
				c.Writer.Header().Add("Retry-After", value)
			}
			c.JSON(status, gin.H{"error": errSelect.Error()})
			return
		}
		if selected == nil {
			if lastResp != nil {
				writeRelayAttemptResponse(c, lastResp)
				return
			}
			if lastErr != nil {
				c.JSON(clienterror.HTTPStatusFromErrorOr(lastErr, http.StatusBadGateway), gin.H{"error": lastErr.Error()})
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta auth unavailable"})
			return
		}
		tried[selected.ID] = struct{}{}

		if exec, okExec := h.authManager.Executor("meta"); okExec && exec != nil {
			if preparer, okPrep := exec.(auth.RequestAuthPreparer); okPrep && preparer.ShouldPrepareRequestAuth(selected) {
				prepareSeq := h.authManager.NextExecutionSeq()
				prepared, errPrep := h.authManager.PrepareRequestAuth(ctx, preparer, selected)
				if errPrep != nil {
					if errCtx := ctx.Err(); errCtx != nil {
						c.JSON(clienterror.HTTPStatusFromErrorOr(errCtx, http.StatusBadGateway), gin.H{"error": errCtx.Error()})
						return
					}
					action, okAction := h.markFailure(ctx, selected, errPrep, nil, prepareSeq)
					if isRequestScopedContinue(action, okAction) {
						lastResp = nil
						lastErr = errPrep
						continue
					}
					c.JSON(clienterror.HTTPStatusFromErrorOr(errPrep, http.StatusBadGateway), gin.H{"error": errPrep.Error()})
					return
				}
				if prepared != nil {
					selected = prepared
				}
			}
		}

		logging.SetGinCPATraceID(c, selected.EnsureIndex())
		respAttempt, action, okAction, errAttempt := h.forwardWithCredential(ctx, cfg, selected, c.Request.Method, c.Request.URL.Path, sanitizedQuery, reqBody, baseReqHeaders, c.Request.Header)
		if errAttempt != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				c.JSON(clienterror.HTTPStatusFromErrorOr(errCtx, http.StatusBadGateway), gin.H{"error": errCtx.Error()})
				return
			}
			if isRequestScopedContinue(action, okAction) {
				lastResp = nil
				lastErr = errAttempt
				continue
			}
			c.JSON(clienterror.HTTPStatusFromErrorOr(errAttempt, http.StatusBadGateway), gin.H{"error": errAttempt.Error()})
			return
		}
		if isRequestScopedContinue(action, okAction) {
			lastResp = respAttempt
			lastErr = nil
			continue
		}
		writeRelayAttemptResponse(c, respAttempt)
		return
	}

	if lastResp != nil {
		writeRelayAttemptResponse(c, lastResp)
		return
	}
	if lastErr != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(lastErr, http.StatusBadGateway), gin.H{"error": lastErr.Error()})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta auth unavailable"})
}

func (h *Handler) forwardWithCredential(
	ctx context.Context,
	cfg *config.Config,
	selected *auth.Auth,
	method string,
	requestPath string,
	sanitizedQuery string,
	reqBody []byte,
	baseReqHeaders http.Header,
	clientHeaders http.Header,
) (*relayAttemptResponse, string, bool, error) {
	alreadyRefreshed := false
	for {
		origin, errOrigin := originFromAuth(selected)
		if errOrigin != nil {
			return nil, "", false, errOrigin
		}

		upstreamURL := origin + requestPath
		if sanitizedQuery != "" {
			upstreamURL += "?" + sanitizedQuery
		}

		upstreamReq, errReq := h.authManager.NewHttpRequest(ctx, selected, method, upstreamURL, reqBody, baseReqHeaders)
		if errReq != nil {
			return nil, "", false, errReq
		}

		authType, authValue := selected.AccountInfo()
		helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
			URL:       upstreamURL,
			Method:    method,
			Headers:   upstreamReq.Header.Clone(),
			Body:      reqBody,
			Provider:  "meta",
			AuthID:    selected.ID,
			AuthLabel: selected.Label,
			AuthType:  authType,
			AuthValue: authValue,
		})

		executionSeq := h.authManager.NextExecutionSeq()
		resp, errDo := h.authManager.HttpRequest(ctx, selected, upstreamReq)
		if errDo != nil {
			helps.RecordAPIResponseError(ctx, cfg, errDo)
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, "", false, errCtx
			}
			action, okAction := h.markFailure(ctx, selected, errDo, nil, executionSeq)
			return nil, action, okAction, errDo
		}

		respBody, errRespRead := readLimitedBody(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("muse code relay: close response body error: %v", errClose)
		}
		helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
		helps.AppendAPIResponseChunk(ctx, cfg, respBody)
		if errRespRead != nil {
			helps.RecordAPIResponseError(ctx, cfg, errRespRead)
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, "", false, errCtx
			}
			if errors.Is(errRespRead, errBodyTooLarge) {
				return nil, "", false, errors.New("Meta response body exceeds 16 MiB limit")
			}
			action, okAction := h.markFailure(ctx, selected, errRespRead, nil, executionSeq)
			return nil, action, okAction, errors.New("Failed to read Meta response")
		}

		if resp.StatusCode == http.StatusUnauthorized && !alreadyRefreshed {
			unauthErr := &auth.Error{HTTPStatus: resp.StatusCode, Message: string(respBody)}
			if refreshed, okRefresh := h.authManager.RefreshAfterUnauthorized(ctx, selected, unauthErr, false, clientHeaders); okRefresh && refreshed != nil {
				selected = refreshed
				alreadyRefreshed = true
				continue
			}
		}

		var action string
		var okAction bool
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
			h.markSuccess(ctx, selected, executionSeq)
		} else if resp.StatusCode >= http.StatusBadRequest {
			logging.SetResponseHeaders(ctx, resp.Header.Clone())
			retryAfter := parseRetryAfterHeader(resp.Header, time.Now())
			action, okAction = h.markHTTPResponseFailure(ctx, selected, resp.StatusCode, string(respBody), retryAfter, executionSeq)
		}

		return &relayAttemptResponse{
			statusCode: resp.StatusCode,
			headers:    resp.Header.Clone(),
			body:       respBody,
		}, action, okAction, nil
	}
}

func (h *Handler) selectMetaAuthExcluding(ctx context.Context, opts coreexecutor.Options, tried map[string]struct{}) (*auth.Auth, error) {
	models := metaCandidateModels(h.authManager)
	if len(models) == 0 {
		return nil, &auth.Error{
			Code:       "auth_not_found",
			Message:    "no available Meta credential",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}

	var firstErr error
	for _, modelID := range models {
		selected, errSelect := h.authManager.SelectAuthExcluding(ctx, "meta", modelID, opts, tried)
		if errSelect == nil && selected != nil {
			return selected, nil
		}
		if errSelect != nil {
			if firstErr == nil || !isAuthNotFoundError(errSelect) {
				firstErr = errSelect
			}
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, &auth.Error{
		Code:       "auth_not_found",
		Message:    "no available Meta credential",
		HTTPStatus: http.StatusServiceUnavailable,
	}
}

func metaCandidateModels(manager *auth.Manager) []string {
	if manager == nil {
		return nil
	}
	reg := registry.GetGlobalRegistry()
	if reg == nil {
		return nil
	}
	auths := manager.List()
	seen := make(map[string]struct{})
	models := make([]string, 0, len(auths)*4)
	for _, candidate := range auths {
		if candidate == nil || candidate.Disabled || candidate.Status == auth.StatusDisabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(candidate.Provider), "meta") {
			continue
		}
		for _, info := range reg.GetModelsForClient(candidate.ID) {
			if info == nil {
				continue
			}
			id := strings.TrimSpace(info.ID)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models
}

func isAuthNotFoundError(err error) bool {
	var authErr *auth.Error
	if errors.As(err, &authErr) && authErr != nil {
		return authErr.Code == "auth_not_found"
	}
	return false
}

func isAllowedEndpoint(method, path string) bool {
	wantMethod, ok := allowedEndpoints[path]
	return ok && strings.EqualFold(strings.TrimSpace(method), wantMethod)
}

func originFromAuth(a *auth.Auth) (string, error) {
	rawBaseURL := ""
	if a != nil {
		if a.Attributes != nil {
			rawBaseURL = strings.TrimSpace(a.Attributes["base_url"])
		}
		if rawBaseURL == "" && a.Metadata != nil {
			if b, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
				rawBaseURL = strings.TrimSpace(b)
			} else if b, ok := a.Metadata["api_base_url"].(string); ok && strings.TrimSpace(b) != "" {
				rawBaseURL = strings.TrimSpace(b)
			}
		}
	}
	if rawBaseURL == "" {
		rawBaseURL = metaauth.DefaultAPIBaseURL
	}
	parsed, errParse := url.Parse(rawBaseURL)
	if errParse != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("muse code: invalid Meta base_url %q", rawBaseURL)
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), nil
}

func sanitizeQuery(rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return ""
	}
	values, errParse := url.ParseQuery(rawQuery)
	if errParse != nil {
		return ""
	}
	values.Del("key")
	values.Del("auth_token")
	return values.Encode()
}

func relayRequestHeaders(in http.Header) http.Header {
	out := make(http.Header)
	if in == nil {
		return out
	}
	for _, name := range allowedRequestHeaders {
		for _, value := range in.Values(name) {
			out.Add(name, value)
		}
	}
	for name, values := range in {
		canonical := http.CanonicalHeaderKey(name)
		if strings.HasPrefix(canonical, "X-Tbh-") || strings.HasPrefix(canonical, "X-Meta-Ai-") {
			for _, value := range values {
				out.Add(canonical, value)
			}
		}
	}
	return out
}

func writeRelayResponseHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	for _, name := range allowedResponseHeaders {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
}

func readLimitedBody(body io.ReadCloser) ([]byte, error) {
	if body == nil || body == http.NoBody {
		return nil, nil
	}
	data, errRead := io.ReadAll(io.LimitReader(body, maxBodySize+1))
	if errRead != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(errRead, &maxBytesErr) {
			return nil, errBodyTooLarge
		}
		return nil, errRead
	}
	if len(data) > maxBodySize {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func shouldMarkCredentialFailure(status int) bool {
	if status >= http.StatusInternalServerError {
		return true
	}
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func parseRetryAfterHeader(headers http.Header, now time.Time) *time.Duration {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds >= 0 {
		if seconds > int64(math.MaxInt64)/int64(time.Second) {
			delay := time.Duration(math.MaxInt64)
			return &delay
		}
		delay := time.Duration(seconds) * time.Second
		return &delay
	}
	if deadline, errParse := http.ParseTime(raw); errParse == nil {
		delay := deadline.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return &delay
	}
	return nil
}

func metaAccessToken(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		for _, key := range []string{"access_token", "accessToken", "api_key"} {
			if value, ok := a.Metadata[key].(string); ok && strings.TrimSpace(value) != "" && !strings.HasPrefix(strings.TrimSpace(value), "dca:") {
				return strings.TrimSpace(value)
			}
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"api_key", "access_token"} {
			if value := strings.TrimSpace(a.Attributes[key]); value != "" && !strings.HasPrefix(value, "dca:") {
				return value
			}
		}
	}
	return ""
}

func (h *Handler) markSuccess(ctx context.Context, current *auth.Auth, executionSeq uint64) {
	if h == nil || h.authManager == nil || current == nil {
		return
	}
	h.authManager.MarkResult(ctx, auth.Result{
		AuthID:            current.ID,
		Provider:          "meta",
		Success:           true,
		AccessToken:       metaAccessToken(current),
		CredentialVersion: current.CredentialVersion,
		RegistrationEpoch: current.RegistrationEpoch,
		ExecutionSeq:      executionSeq,
	})
}

func (h *Handler) markHTTPResponseFailure(ctx context.Context, current *auth.Auth, statusCode int, body string, retryAfter *time.Duration, executionSeq uint64) (string, bool) {
	if h == nil || h.authManager == nil || current == nil {
		return "", false
	}
	rawErr := &auth.Error{HTTPStatus: statusCode, Message: body}
	result := auth.Result{
		AuthID:            current.ID,
		Provider:          "meta",
		Success:           false,
		CredentialScope:   true,
		AccessToken:       metaAccessToken(current),
		RetryAfter:        retryAfter,
		Error:             &auth.Error{HTTPStatus: statusCode, Message: body},
		CredentialVersion: current.CredentialVersion,
		RegistrationEpoch: current.RegistrationEpoch,
		ExecutionSeq:      executionSeq,
	}
	action, okAction := h.authManager.ApplyRequestScopedErrorResult(ctx, current, rawErr, &result)
	isForceCooldown := okAction && (action == auth.RequestScopedActionStopAndCooldown || action == auth.RequestScopedActionContinueAndCooldown)
	if !shouldMarkCredentialFailure(statusCode) && !isForceCooldown {
		return action, okAction
	}
	h.authManager.MarkResult(ctx, result)
	return action, okAction
}

func (h *Handler) markFailure(ctx context.Context, current *auth.Auth, err error, retryAfter *time.Duration, executionSeq uint64) (string, bool) {
	if h == nil || h.authManager == nil || current == nil || err == nil {
		return "", false
	}
	authErr := auth.ResultErrorFromError(err)
	result := auth.Result{
		AuthID:            current.ID,
		Provider:          "meta",
		Success:           false,
		CredentialScope:   true,
		AccessToken:       metaAccessToken(current),
		RetryAfter:        retryAfter,
		Error:             authErr,
		CredentialVersion: current.CredentialVersion,
		RegistrationEpoch: current.RegistrationEpoch,
		ExecutionSeq:      executionSeq,
	}
	action, okAction := h.authManager.ApplyRequestScopedErrorResult(ctx, current, err, &result)
	h.authManager.MarkResult(ctx, result)
	return action, okAction
}
