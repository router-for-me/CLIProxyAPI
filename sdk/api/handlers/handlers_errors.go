package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/context"
)

func statusFromError(err error) int {
	return clienterror.HTTPStatusFromError(err)
}

func isAuthSelectionUnavailable(err error) bool {
	type modelCooldownMarker interface {
		IsModelCooldown() bool
	}
	var mcm modelCooldownMarker
	if errors.As(err, &mcm) && mcm != nil && mcm.IsModelCooldown() {
		return false
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	code := strings.TrimSpace(authErr.Code)
	return code == "auth_not_found" || code == "auth_unavailable"
}

func enrichAuthSelectionError(err error, providers []string, model string) error {
	if err == nil {
		return nil
	}

	type modelCooldownMarker interface {
		IsModelCooldown() bool
	}
	var mcm modelCooldownMarker
	if errors.As(err, &mcm) && mcm != nil && mcm.IsModelCooldown() {
		return err
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil {
		return err
	}

	code := strings.TrimSpace(authErr.Code)
	if code != "auth_not_found" && code != "auth_unavailable" {
		return err
	}

	providerText := strings.Join(providers, ",")
	if providerText == "" {
		providerText = "unknown"
	}
	modelText := strings.TrimSpace(model)
	if modelText == "" {
		modelText = "unknown"
	}

	baseMessage := strings.TrimSpace(authErr.Message)
	if baseMessage == "" {
		baseMessage = "no auth available"
	}

	cause := errors.Unwrap(err)
	var upstreamSummary string
	if cause != nil {
		upstreamSummary = coreauth.ExtractUpstreamErrorSummary(cause.Error())
	}
	var detail string
	if upstreamSummary != "" && !strings.Contains(baseMessage, upstreamSummary) {
		detail = fmt.Sprintf("%s (providers=%s, model=%s; last upstream error: %s)", baseMessage, providerText, modelText, upstreamSummary)
	} else {
		detail = fmt.Sprintf("%s (providers=%s, model=%s)", baseMessage, providerText, modelText)
	}

	// Clarify the most common alias confusion between Anthropic route names and internal provider keys.
	if strings.Contains(","+providerText+",", ",claude,") {
		detail += "; check Claude auth/key session and cooldown state via /v0/management/auth-files"
	}

	status := authErr.HTTPStatus
	if status <= 0 {
		status = http.StatusServiceUnavailable
	}

	enriched := &coreauth.Error{
		Code:       authErr.Code,
		Message:    detail,
		Retryable:  authErr.Retryable,
		HTTPStatus: status,
	}
	if cause != nil {
		return coreauth.WithCause(enriched, cause)
	}
	return enriched
}

// WriteErrorResponse writes an error message to the response writer using the HTTP status embedded in the message.
func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.DirectResponse {
		writeDirectErrorResponse(c, status, msg)
		return
	}
	if msg != nil && msg.Error != nil {
		for _, value := range coreauth.SafeResponseHeaders(msg.Error).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
	}
	if msg != nil && msg.Addon != nil && PassthroughHeadersEnabled(h.Cfg) {
		for key, values := range msg.Addon {
			if len(values) == 0 || IsCPAReservedResponseHeader(key) {
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

func writeDirectErrorResponse(c *gin.Context, status int, msg *interfaces.ErrorMessage) {
	for key, values := range FilterUpstreamHeaders(msg.Headers) {
		if len(values) == 0 || IsCPAReservedResponseHeader(key) {
			continue
		}
		c.Writer.Header().Del(key)
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
	body := bytes.Clone(msg.Body)
	appendAPIResponse(c, body)
	if !c.Writer.Written() && c.Writer.Header().Get("Content-Type") == "" {
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
