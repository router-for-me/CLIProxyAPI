// Package handlers provides core API handler functionality for the CLI Proxy API server.
// It includes common types, client management, load balancing, and error handling
// shared across all API endpoint handlers (OpenAI, Claude, Gemini).
package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	// Error contains detailed information about the error that occurred.
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// Message is a human-readable message providing more details about the error.
	Message string `json:"message"`

	// Type is the category of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`

	// Code is a short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}

const idempotencyKeyMetadataKey = "idempotency_key"

const (
	defaultStreamingKeepAliveSeconds = 0
	defaultStreamingBootstrapRetries = 0
	maxThinkingFallbackRetries       = 6
)

type pinnedAuthContextKey struct{}
type selectedAuthCallbackContextKey struct{}
type executionSessionContextKey struct{}

// WithPinnedAuthID returns a child context that requests execution on a specific auth ID.
func WithPinnedAuthID(ctx context.Context, authID string) context.Context {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pinnedAuthContextKey{}, authID)
}

// WithSelectedAuthIDCallback returns a child context that receives the selected auth ID.
func WithSelectedAuthIDCallback(ctx context.Context, callback func(string)) context.Context {
	if callback == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectedAuthCallbackContextKey{}, callback)
}

// WithExecutionSessionID returns a child context tagged with a long-lived execution session ID.
func WithExecutionSessionID(ctx context.Context, sessionID string) context.Context {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionSessionContextKey{}, sessionID)
}

// BuildErrorResponseBody builds an OpenAI-compatible JSON error response body.
// If errText is already valid JSON, it is returned as-is to preserve upstream error payloads.
func BuildErrorResponseBody(status int, errText string) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(errText) == "" {
		errText = http.StatusText(status)
	}

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return []byte(trimmed)
	}

	errType := "invalid_request_error"
	var code string
	switch status {
	case http.StatusUnauthorized:
		errType = "authentication_error"
		code = "invalid_api_key"
	case http.StatusForbidden:
		errType = "permission_error"
		code = "insufficient_quota"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
		code = "rate_limit_exceeded"
	case http.StatusNotFound:
		errType = "invalid_request_error"
		code = "model_not_found"
	default:
		if status >= http.StatusInternalServerError {
			errType = "server_error"
			code = "internal_server_error"
		}
	}

	payload, err := json.Marshal(ErrorResponse{
		Error: ErrorDetail{
			Message: errText,
			Type:    errType,
			Code:    code,
		},
	})
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, errText))
	}
	return payload
}

// StreamingKeepAliveInterval returns the SSE keep-alive interval for this server.
// Returning 0 disables keep-alives (default when unset).
func StreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := defaultStreamingKeepAliveSeconds
	if cfg != nil {
		seconds = cfg.Streaming.KeepAliveSeconds
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// NonStreamingKeepAliveInterval returns the keep-alive interval for non-streaming responses.
// Returning 0 disables keep-alives (default when unset).
func NonStreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := 0
	if cfg != nil {
		seconds = cfg.NonStreamKeepAliveInterval
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// StreamingBootstrapRetries returns how many times a streaming request may be retried before any bytes are sent.
func StreamingBootstrapRetries(cfg *config.SDKConfig) int {
	retries := defaultStreamingBootstrapRetries
	if cfg != nil {
		retries = cfg.Streaming.BootstrapRetries
	}
	if retries < 0 {
		retries = 0
	}
	return retries
}

// PassthroughHeadersEnabled returns whether upstream response headers should be forwarded to clients.
// Default is false.
func PassthroughHeadersEnabled(cfg *config.SDKConfig) bool {
	return cfg != nil && cfg.PassthroughHeaders
}

func requestExecutionMetadata(ctx context.Context) map[string]any {
	// Idempotency-Key is an optional client-supplied header used to correlate retries.
	// It is forwarded as execution metadata; when absent we generate a UUID.
	key := ""
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			key = strings.TrimSpace(ginCtx.GetHeader("Idempotency-Key"))
		}
	}
	if key == "" {
		key = uuid.NewString()
	}

	meta := map[string]any{idempotencyKeyMetadataKey: key}
	if pinnedAuthID := pinnedAuthIDFromContext(ctx); pinnedAuthID != "" {
		meta[coreexecutor.PinnedAuthMetadataKey] = pinnedAuthID
	}
	if selectedCallback := selectedAuthIDCallbackFromContext(ctx); selectedCallback != nil {
		meta[coreexecutor.SelectedAuthCallbackMetadataKey] = selectedCallback
	}
	if executionSessionID := executionSessionIDFromContext(ctx); executionSessionID != "" {
		meta[coreexecutor.ExecutionSessionMetadataKey] = executionSessionID
	}
	return meta
}

func pinnedAuthIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(pinnedAuthContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func selectedAuthIDCallbackFromContext(ctx context.Context) func(string) {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(selectedAuthCallbackContextKey{})
	if callback, ok := raw.(func(string)); ok && callback != nil {
		return callback
	}
	return nil
}

func executionSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(executionSessionContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

// BaseAPIHandler contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service and manages
// load balancing, client selection, and configuration.
type BaseAPIHandler struct {
	// AuthManager manages auth lifecycle and execution in the new architecture.
	AuthManager *coreauth.Manager

	// Cfg holds the current application configuration.
	Cfg *config.SDKConfig
}

// NewBaseAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and configuration as input.
//
// Parameters:
//   - cliClients: A slice of AI service clients
//   - cfg: The application configuration
//
// Returns:
//   - *BaseAPIHandler: A new API handlers instance
func NewBaseAPIHandlers(cfg *config.SDKConfig, authManager *coreauth.Manager) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:         cfg,
		AuthManager: authManager,
	}
}

// UpdateClients updates the handlers' client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) { h.Cfg = cfg }

// GetAlt extracts the 'alt' parameter from the request query string.
// It checks both 'alt' and '$alt' parameters and returns the appropriate value.
//
// Parameters:
//   - c: The Gin context containing the HTTP request
//
// Returns:
//   - string: The alt parameter value, or empty string if it's "sse"
func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	var alt string
	var hasAlt bool
	alt, hasAlt = c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

// GetContextWithCancel creates a new context with cancellation capabilities.
// It embeds the Gin context and the API handler into the new context for later use.
// The returned cancel function also handles logging the API response if request logging is enabled.
//
// Parameters:
//   - handler: The API handler associated with the request.
//   - c: The Gin context of the current request.
//   - ctx: The parent context (caller values/deadlines are preserved; request context adds cancellation and request ID).
//
// Returns:
//   - context.Context: The new context with cancellation and embedded values.
//   - APIHandlerCancelFunc: A function to cancel the context and log the response.
func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	var requestCtx context.Context
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}

	if requestCtx != nil && logging.GetRequestID(parentCtx) == "" {
		if requestID := logging.GetRequestID(requestCtx); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		} else if requestID := logging.GetGinRequestID(c); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		}
	}
	newCtx, cancel := context.WithCancel(parentCtx)
	cancelCtx := newCtx
	if requestCtx != nil && requestCtx != parentCtx {
		go func() {
			select {
			case <-requestCtx.Done():
				cancel()
			case <-cancelCtx.Done():
			}
		}()
	}
	newCtx = context.WithValue(newCtx, "gin", c)
	newCtx = context.WithValue(newCtx, "handler", handler)
	return newCtx, func(params ...interface{}) {
		if h.Cfg.RequestLog && len(params) == 1 {
			if existing, exists := c.Get("API_RESPONSE"); exists {
				if existingBytes, ok := existing.([]byte); ok && len(bytes.TrimSpace(existingBytes)) > 0 {
					switch params[0].(type) {
					case error, string:
						cancel()
						return
					}
				}
			}

			var payload []byte
			switch data := params[0].(type) {
			case []byte:
				payload = data
			case error:
				if data != nil {
					payload = []byte(data.Error())
				}
			case string:
				payload = []byte(data)
			}
			if len(payload) > 0 {
				if existing, exists := c.Get("API_RESPONSE"); exists {
					if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
						trimmedPayload := bytes.TrimSpace(payload)
						if len(trimmedPayload) > 0 && bytes.Contains(existingBytes, trimmedPayload) {
							cancel()
							return
						}
					}
				}
				appendAPIResponse(c, payload)
			}
		}

		cancel()
	}
}

// StartNonStreamingKeepAlive emits blank lines every 5 seconds while waiting for a non-streaming response.
// It returns a stop function that must be called before writing the final response.
func (h *BaseAPIHandler) StartNonStreamingKeepAlive(c *gin.Context, ctx context.Context) func() {
	if h == nil || c == nil {
		return func() {}
	}
	interval := NonStreamingKeepAliveInterval(h.Cfg)
	if interval <= 0 {
		return func() {}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stopChan := make(chan struct{})
	var stopOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
		wg.Wait()
	}
}

// appendAPIResponse preserves any previously captured API response and appends new data.
func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}

	// Capture timestamp on first API response
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); !exists {
		c.Set("API_RESPONSE_TIMESTAMP", time.Now())
	}

	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}

	c.Set("API_RESPONSE", bytes.Clone(data))
}

func extractThinkingFallbackMessage(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		return ""
	}
	parse := func(candidate string) string {
		if candidate == "" || !gjson.Valid(candidate) {
			return ""
		}
		if msg := strings.TrimSpace(gjson.Get(candidate, "error.message").String()); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(gjson.Get(candidate, "message").String()); msg != "" {
			return msg
		}
		return ""
	}
	if msg := parse(raw); msg != "" {
		return msg
	}
	if idx := strings.Index(raw, "{"); idx >= 0 && idx < len(raw) {
		if msg := parse(strings.TrimSpace(raw[idx:])); msg != "" {
			return msg
		}
	}
	return raw
}

func shouldFallbackThinkingEffort(err error) bool {
	if err == nil {
		return false
	}
	var thinkingErr *thinking.ThinkingError
	if errors.As(err, &thinkingErr) && thinkingErr != nil {
		switch thinkingErr.Code {
		case thinking.ErrLevelNotSupported, thinking.ErrBudgetOutOfRange:
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(extractThinkingFallbackMessage(err)))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "valid levels") && strings.Contains(msg, "not supported") {
		return true
	}
	hasThinkingKeyword := strings.Contains(msg, "reasoning_effort") ||
		strings.Contains(msg, "reasoning.effort") ||
		(strings.Contains(msg, "reasoning") && strings.Contains(msg, "effort")) ||
		strings.Contains(msg, "thinking")
	hasUnsupportedSignal := strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "out of range") ||
		strings.Contains(msg, "invalid")
	return hasThinkingKeyword && hasUnsupportedSignal
}

func nextLowerThinkingEffort(current string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(current)) {
	case "max":
		return "xhigh", true
	case "xhigh":
		return "high", true
	case "high":
		return "medium", true
	case "medium":
		return "low", true
	case "low":
		return "minimal", true
	case "minimal":
		return "none", true
	default:
		return "", false
	}
}

func parseThinkingEffort(req coreexecutor.Request) (string, bool) {
	suffix := thinking.ParseSuffix(req.Model)
	if suffix.HasSuffix {
		if level, ok := thinking.ParseLevelSuffix(suffix.RawSuffix); ok {
			return strings.ToLower(string(level)), true
		}
	}
	if len(req.Payload) == 0 || !gjson.ValidBytes(req.Payload) {
		return "", false
	}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		value := strings.ToLower(strings.TrimSpace(gjson.GetBytes(req.Payload, path).String()))
		if value == "" {
			continue
		}
		switch value {
		case "max", "xhigh", "high", "medium", "low", "minimal", "none":
			return value, true
		}
	}
	return "", false
}

func rewriteThinkingEffortInPayload(payload []byte, effort string, modelName string) ([]byte, bool) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}
	out := bytes.Clone(payload)
	changed := false
	setString := func(path, value string) {
		if !gjson.GetBytes(out, path).Exists() {
			return
		}
		updated, err := sjson.SetBytes(out, path, value)
		if err != nil {
			return
		}
		out = updated
		changed = true
	}
	setString("reasoning.effort", effort)
	setString("reasoning_effort", effort)
	setString("output_config.effort", effort)
	if modelName != "" && gjson.GetBytes(out, "model").Exists() {
		updated, err := sjson.SetBytes(out, "model", modelName)
		if err == nil {
			out = updated
			changed = true
		}
	}
	return out, changed
}

func downgradeRequestThinkingEffort(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options, cause error) (coreexecutor.Request, coreexecutor.Options, bool) {
	current, ok := parseThinkingEffort(req)
	if !ok {
		return req, opts, false
	}
	next, ok := nextLowerThinkingEffort(current)
	if !ok {
		return req, opts, false
	}

	newReq := req
	newOpts := opts
	changed := false

	suffix := thinking.ParseSuffix(req.Model)
	if suffix.HasSuffix {
		if _, ok := thinking.ParseLevelSuffix(suffix.RawSuffix); ok {
			base := strings.TrimSpace(suffix.ModelName)
			if base != "" {
				newReq.Model = fmt.Sprintf("%s(%s)", base, next)
				changed = true
			}
		}
	}

	if updatedPayload, payloadChanged := rewriteThinkingEffortInPayload(newReq.Payload, next, newReq.Model); payloadChanged {
		newReq.Payload = updatedPayload
		changed = true
	}
	if len(newOpts.OriginalRequest) > 0 {
		if updatedOriginal, originalChanged := rewriteThinkingEffortInPayload(newOpts.OriginalRequest, next, newReq.Model); originalChanged {
			newOpts.OriginalRequest = updatedOriginal
			changed = true
		}
	}

	if !changed {
		return req, opts, false
	}

	log.WithFields(log.Fields{
		"request_id": logging.GetRequestID(ctx),
		"providers":  strings.Join(providers, ","),
		"model":      req.Model,
		"from":       current,
		"to":         next,
		"error":      strings.TrimSpace(extractThinkingFallbackMessage(cause)),
	}).Warn("thinking: effort fallback applied for request retry |")
	return newReq, newOpts, true
}

// ExecuteWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	providers, normalizedModel, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = normalizedModel
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	opts.Metadata = reqMeta
	var (
		resp coreexecutor.Response
		err  error
	)
	thinkingFallbackRetries := 0
	for {
		if reqMeta != nil {
			reqMeta[coreexecutor.RequestedModelMetadataKey] = req.Model
		}
		resp, err = h.AuthManager.Execute(ctx, providers, req, opts)
		if err == nil {
			break
		}
		if thinkingFallbackRetries < maxThinkingFallbackRetries && shouldFallbackThinkingEffort(err) {
			if nextReq, nextOpts, ok := downgradeRequestThinkingEffort(ctx, providers, req, opts, err); ok {
				req, opts = nextReq, nextOpts
				thinkingFallbackRetries++
				continue
			}
		}
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		publishHandlerFailureUsage(ctx, strings.Join(providers, ","), req.Model, status, err)
		return nil, nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	if !PassthroughHeadersEnabled(h.Cfg) {
		return resp.Payload, nil, nil
	}
	return resp.Payload, FilterUpstreamHeaders(resp.Headers), nil
}

// ExecuteCountWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	providers, normalizedModel, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = normalizedModel
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	opts.Metadata = reqMeta
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		return nil, nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	if !PassthroughHeadersEnabled(h.Cfg) {
		return resp.Payload, nil, nil
	}
	return resp.Payload, FilterUpstreamHeaders(resp.Headers), nil
}

// ExecuteStreamWithAuthManager executes a streaming request via the core auth manager.
// This path is the only supported execution route.
// The returned http.Header carries upstream response headers captured before streaming begins.
func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	providers, normalizedModel, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, nil, errChan
	}
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = normalizedModel
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          true,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	opts.Metadata = reqMeta
	var (
		streamResult *coreexecutor.StreamResult
		err          error
	)
	thinkingFallbackRetries := 0
	for {
		if reqMeta != nil {
			reqMeta[coreexecutor.RequestedModelMetadataKey] = req.Model
		}
		streamResult, err = h.AuthManager.ExecuteStream(ctx, providers, req, opts)
		if err == nil {
			break
		}
		if thinkingFallbackRetries < maxThinkingFallbackRetries && shouldFallbackThinkingEffort(err) {
			if nextReq, nextOpts, ok := downgradeRequestThinkingEffort(ctx, providers, req, opts, err); ok {
				req, opts = nextReq, nextOpts
				thinkingFallbackRetries++
				continue
			}
		}
		errChan := make(chan *interfaces.ErrorMessage, 1)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		publishHandlerFailureUsage(ctx, strings.Join(providers, ","), req.Model, status, err)
		errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
		close(errChan)
		return nil, nil, errChan
	}
	passthroughHeadersEnabled := PassthroughHeadersEnabled(h.Cfg)
	// Capture upstream headers from the initial connection synchronously before the goroutine starts.
	// Keep a mutable map so bootstrap retries can replace it before first payload is sent.
	var upstreamHeaders http.Header
	if passthroughHeadersEnabled {
		upstreamHeaders = cloneHeader(FilterUpstreamHeaders(streamResult.Headers))
		if upstreamHeaders == nil {
			upstreamHeaders = make(http.Header)
		}
	}
	chunks := streamResult.Chunks
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		sentPayload := false
		bootstrapRetries := 0
		maxBootstrapRetries := StreamingBootstrapRetries(h.Cfg)
		failureDetector := &streamFailureDetector{}
		var responsesLifecycle *openAIResponsesStreamLifecycle
		var responsesSSEValidationCarry []byte
		var responsesPayloadCarry []byte
		if handlerType == "openai-response" {
			responsesLifecycle = &openAIResponsesStreamLifecycle{}
		}
		resetPrePayloadState := func() {
			failureDetector = &streamFailureDetector{}
			if handlerType != "openai-response" {
				return
			}
			// We have not emitted anything downstream yet; discard partial parser/lifecycle
			// state before switching to a fresh upstream stream.
			responsesSSEValidationCarry = nil
			responsesPayloadCarry = nil
			responsesLifecycle = &openAIResponsesStreamLifecycle{}
		}

		sendErr := func(msg *interfaces.ErrorMessage) bool {
			if ctx == nil {
				errChan <- msg
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case errChan <- msg:
				return true
			}
		}

		sendData := func(chunk []byte) bool {
			if ctx == nil {
				dataChan <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case dataChan <- chunk:
				return true
			}
		}

		bootstrapEligible := func(err error) bool {
			if exhausted, ok := err.(interface{ BootstrapRetryExhausted() bool }); ok && exhausted.BootstrapRetryExhausted() {
				return false
			}
			status := statusFromError(err)
			if status == 0 {
				if isAuthAvailabilityError(err) {
					return false
				}
				return true
			}
			if isAuthAvailabilityError(err) {
				return false
			}
			switch status {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired,
				http.StatusRequestTimeout, http.StatusTooManyRequests:
				return true
			default:
				return status >= http.StatusInternalServerError
			}
		}

	outer:
		for {
			var chunk coreexecutor.StreamChunk
			var ok bool
			if ctx != nil {
				select {
				case <-ctx.Done():
					return
				case chunk, ok = <-chunks:
				}
			} else {
				chunk, ok = <-chunks
			}

			if !ok {
				if handlerType == "openai-response" {
					if err := validateSSEDataJSONWithCarry(&responsesSSEValidationCarry, nil, true); err != nil {
						publishHandlerFailureUsage(ctx, strings.Join(providers, ","), req.Model, http.StatusBadGateway, err)
						_ = sendErr(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
						return
					}
					if len(responsesPayloadCarry) > 0 {
						sentPayload = true
						if okSendData := sendData(cloneBytes(responsesPayloadCarry)); !okSendData {
							return
						}
						responsesPayloadCarry = nil
					}
				}
				if responsesLifecycle != nil && sentPayload && responsesLifecycle.NeedsSyntheticCompletion() {
					if synthetic := responsesLifecycle.SyntheticCompletionChunk(); len(synthetic) > 0 {
						if okSendData := sendData(synthetic); !okSendData {
							return
						}
					}
				}
				return
			}

			if chunk.Err != nil {
				streamErr := chunk.Err
				// Safe bootstrap recovery: if the upstream fails before any payload bytes are sent,
				// retry a few times (to allow auth rotation / transient recovery) and then attempt model fallback.
				if !sentPayload {
					if bootstrapRetries < maxBootstrapRetries && bootstrapEligible(streamErr) {
						bootstrapRetries++
						resetPrePayloadState()
						if reqMeta != nil {
							reqMeta[coreexecutor.RequestedModelMetadataKey] = req.Model
						}
						retryResult, retryErr := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
						if retryErr == nil {
							if passthroughHeadersEnabled {
								replaceHeader(upstreamHeaders, FilterUpstreamHeaders(retryResult.Headers))
							}
							chunks = retryResult.Chunks
							continue outer
						}
						streamErr = retryErr
					}

					for thinkingFallbackRetries < maxThinkingFallbackRetries && shouldFallbackThinkingEffort(streamErr) {
						nextReq, nextOpts, okFallback := downgradeRequestThinkingEffort(ctx, providers, req, opts, streamErr)
						if !okFallback {
							break
						}
						req, opts = nextReq, nextOpts
						thinkingFallbackRetries++
						resetPrePayloadState()
						if reqMeta != nil {
							reqMeta[coreexecutor.RequestedModelMetadataKey] = req.Model
						}
						retryResult, retryErr := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
						if retryErr == nil {
							if passthroughHeadersEnabled {
								replaceHeader(upstreamHeaders, FilterUpstreamHeaders(retryResult.Headers))
							}
							chunks = retryResult.Chunks
							bootstrapRetries = 0
							continue outer
						}
						streamErr = retryErr
					}
				}

				status := http.StatusInternalServerError
				if se, ok := streamErr.(interface{ StatusCode() int }); ok && se != nil {
					if code := se.StatusCode(); code > 0 {
						status = code
					}
				}
				var addon http.Header
				if he, ok := streamErr.(interface{ Headers() http.Header }); ok && he != nil {
					if hdr := he.Headers(); hdr != nil {
						addon = hdr.Clone()
					}
				}
				publishHandlerFailureUsage(ctx, strings.Join(providers, ","), req.Model, status, streamErr)
				_ = sendErr(&interfaces.ErrorMessage{StatusCode: status, Error: streamErr, Addon: addon})
				return
			}

			if len(chunk.Payload) > 0 {
				if err := failureDetector.Observe(chunk.Payload); err != nil {
					publishHandlerFailureUsage(ctx, strings.Join(providers, ","), req.Model, http.StatusBadGateway, err)
					_ = sendErr(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
					return
				}
				if responsesLifecycle != nil {
					responsesLifecycle.Observe(chunk.Payload)
				}
				if handlerType == "openai-response" {
					if err := validateSSEDataJSONWithCarry(&responsesSSEValidationCarry, chunk.Payload, false); err != nil {
						publishHandlerFailureUsage(ctx, strings.Join(providers, ","), req.Model, http.StatusBadGateway, err)
						_ = sendErr(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
						return
					}
					if len(responsesSSEValidationCarry) > 0 {
						responsesPayloadCarry = append(responsesPayloadCarry, chunk.Payload...)
						continue
					}
					if len(responsesPayloadCarry) > 0 {
						responsesPayloadCarry = append(responsesPayloadCarry, chunk.Payload...)
						sentPayload = true
						if okSendData := sendData(cloneBytes(responsesPayloadCarry)); !okSendData {
							return
						}
						responsesPayloadCarry = nil
						continue
					}
				}
				sentPayload = true
				if okSendData := sendData(cloneBytes(chunk.Payload)); !okSendData {
					return
				}
			}
		}
	}()
	return dataChan, upstreamHeaders, errChan
}

func validateSSEDataJSONWithCarry(carry *[]byte, chunk []byte, finalize bool) error {
	if carry == nil {
		tmp := []byte(nil)
		carry = &tmp
	}
	buf := make([]byte, 0, len(*carry)+len(chunk))
	buf = append(buf, *carry...)
	buf = append(buf, chunk...)
	*carry = nil

	lines := bytes.Split(buf, []byte("\n"))
	if len(lines) == 0 {
		return nil
	}
	endsWithNewline := len(buf) > 0 && buf[len(buf)-1] == '\n'
	lastIdx := len(lines) - 1
	limit := len(lines)
	if !finalize && !endsWithNewline {
		limit = lastIdx
	}
	for i := 0; i < limit; i++ {
		if err := validateSSEDataJSONLine(lines[i]); err != nil {
			return err
		}
	}

	if finalize || endsWithNewline {
		if !endsWithNewline && lastIdx >= 0 {
			if err := validateSSEDataJSONLine(lines[lastIdx]); err != nil {
				return err
			}
		}
		*carry = nil
		return nil
	}

	tail := bytes.TrimRight(lines[lastIdx], "\r")
	trimmed := bytes.TrimSpace(tail)
	if len(trimmed) == 0 {
		*carry = nil
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		data := bytes.TrimSpace(trimmed[5:])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || json.Valid(data) {
			*carry = nil
			return nil
		}
	}
	*carry = bytes.Clone(tail)
	return nil
}

func validateSSEDataJSONLine(line []byte) error {
	line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
	if len(line) == 0 {
		return nil
	}
	if bytes.HasPrefix(line, []byte(":")) ||
		bytes.HasPrefix(line, []byte("event:")) ||
		bytes.HasPrefix(line, []byte("id:")) ||
		bytes.HasPrefix(line, []byte("retry:")) {
		return nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil
	}
	data := bytes.TrimSpace(line[5:])
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	if json.Valid(data) {
		return nil
	}
	const max = 512
	preview := data
	if len(preview) > max {
		preview = preview[:max]
	}
	return fmt.Errorf("invalid SSE data JSON (len=%d): %q", len(data), preview)
}

func validateSSEDataJSON(chunk []byte) error {
	var carry []byte
	return validateSSEDataJSONWithCarry(&carry, chunk, true)
}

func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
		if code := se.StatusCode(); code > 0 {
			return code
		}
	}
	return 0
}

func publishHandlerFailureUsage(ctx context.Context, provider, model string, status int, err error) {
	coreusage.MarkRequestFailed(ctx)
	if coreusage.RecordPublished(ctx) {
		return
	}
	errorCode, errorMessage, resolvedStatus := resolveHandlerUsageError(err, status)
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:      strings.TrimSpace(provider),
		Model:         strings.TrimSpace(model),
		APIKey:        handlerAPIKeyFromContext(ctx),
		RequestID:     handlerRequestIDFromContext(ctx),
		RequestLogRef: handlerRequestIDFromContext(ctx),
		RequestedAt:   time.Now(),
		Failed:        true,
		FailureStage:  resolveHandlerFailureStage(err),
		ErrorCode:     errorCode,
		ErrorMessage:  errorMessage,
		StatusCode:    resolvedStatus,
	})
}

func resolveHandlerFailureStage(err error) string {
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		switch strings.TrimSpace(authErr.Code) {
		case "auth_unavailable", "auth_not_found":
			return "auth_selection"
		}
	}
	return "request_execution"
}

func resolveHandlerUsageError(err error, fallbackStatus int) (code, message string, status int) {
	status = fallbackStatus
	if err == nil {
		return "", "", status
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		code = strings.TrimSpace(authErr.Code)
		message = strings.TrimSpace(authErr.Message)
		if resolved := authErr.StatusCode(); resolved > 0 {
			status = resolved
		}
		if message == "" {
			message = strings.TrimSpace(authErr.Error())
		}
		return code, message, status
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
		if resolved := se.StatusCode(); resolved > 0 {
			status = resolved
		}
	}
	return "", strings.TrimSpace(err.Error()), status
}

func handlerAPIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	value, exists := ginCtx.Get("apiKey")
	if !exists {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func handlerRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if requestID := strings.TrimSpace(logging.GetRequestID(ctx)); requestID != "" {
		return requestID
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	return strings.TrimSpace(logging.GetGinRequestID(ginCtx))
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
		if initialSuffix.HasSuffix {
			resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
		} else {
			resolvedModelName = resolvedBase
		}
	} else {
		resolvedModelName = util.ResolveAutoModel(modelName)
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	providers = util.GetProviderName(baseModel)
	// Fallback: if baseModel has no provider but differs from resolvedModelName,
	// try using the full model name. This handles edge cases where custom models
	// may be registered with their full suffixed name (e.g., "my-model(8192)").
	// Evaluated in Story 11.8: This fallback is intentionally preserved to support
	// custom model registrations that include thinking suffixes.
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}

	if len(providers) == 0 {
		return nil, "", &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}

	// The thinking suffix is preserved in the model name itself, so no
	// metadata-based configuration passing is needed.
	return providers, resolvedModelName, nil
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func replaceHeader(dst http.Header, src http.Header) {
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}

// WriteErrorResponse writes an error message to the response writer using the HTTP status embedded in the message.
func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.Addon != nil && PassthroughHeadersEnabled(h.Cfg) {
		for key, values := range msg.Addon {
			if len(values) == 0 {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}

	errText := http.StatusText(status)
	if msg != nil && msg.Error != nil {
		if v := strings.TrimSpace(msg.Error.Error()); v != "" {
			errText = v
		}
	}

	body := BuildErrorResponseBody(status, errText)
	// Append first to preserve upstream response logs, then drop duplicate payloads if already recorded.
	var previous []byte
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			previous = existingBytes
		}
	}
	appendAPIResponse(c, body)
	trimmedErrText := strings.TrimSpace(errText)
	trimmedBody := bytes.TrimSpace(body)
	if len(previous) > 0 {
		if (trimmedErrText != "" && bytes.Contains(previous, []byte(trimmedErrText))) ||
			(len(trimmedBody) > 0 && bytes.Contains(previous, trimmedBody)) {
			c.Set("API_RESPONSE", previous)
		}
	}

	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Status(status)
	_, _ = c.Writer.Write(body)
}

func (h *BaseAPIHandler) LoggingAPIResponseError(ctx context.Context, err *interfaces.ErrorMessage) {
	if h.Cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			if apiResponseErrors, isExist := ginContext.Get("API_RESPONSE_ERROR"); isExist {
				if slicesAPIResponseError, isOk := apiResponseErrors.([]*interfaces.ErrorMessage); isOk {
					slicesAPIResponseError = append(slicesAPIResponseError, err)
					ginContext.Set("API_RESPONSE_ERROR", slicesAPIResponseError)
				}
			} else {
				// Create new response data entry
				ginContext.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{err})
			}
		}
	}
}

// APIHandlerCancelFunc is a function type for canceling an API handler's context.
// It can optionally accept parameters, which are used for logging the response.
type APIHandlerCancelFunc func(params ...interface{})
