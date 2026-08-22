package claude

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const claudeStreamBootstrapDiagnosticValueLimit = 512

func logClaudeStreamBootstrapDiagnostic(c *gin.Context, ctx context.Context, phase, model string, msg *interfaces.ErrorMessage) {
	fields, ok := claudeStreamBootstrapDiagnosticFields(c, ctx, phase, model, msg)
	if !ok {
		return
	}
	log.WithFields(fields).Warn("claude stream bootstrap 503")
}

func claudeStreamBootstrapDiagnosticFields(c *gin.Context, ctx context.Context, phase, model string, msg *interfaces.ErrorMessage) (log.Fields, bool) {
	if msg == nil || msg.StatusCode != http.StatusServiceUnavailable {
		return nil, false
	}

	authCode := ""
	var authErr *coreauth.Error
	if errors.As(msg.Error, &authErr) && authErr != nil {
		authCode = authErr.Code
	}

	upstreamHeaders := internallogging.GetResponseHeaders(ctx)
	safeHeaders := coreauth.SafeResponseHeaders(msg.Error)
	fields := log.Fields{
		"request_id":             sanitizeClaudeBootstrapDiagnosticValue(internallogging.GetGinRequestID(c)),
		"phase":                  sanitizeClaudeBootstrapDiagnosticValue(phase),
		"model":                  sanitizeClaudeBootstrapDiagnosticValue(model),
		"status":                 msg.StatusCode,
		"auth_selected":          internallogging.GetGinCPATraceID(c) != "",
		"upstream_response_seen": len(upstreamHeaders) > 0 || msg.DirectResponse || claudeBootstrapErrorHasHeaders(msg.Error),
		"auth_code":              sanitizeClaudeBootstrapDiagnosticValue(authCode),
		"retry_after": sanitizeClaudeBootstrapDiagnosticValue(firstClaudeBootstrapDiagnosticHeader(
			[]http.Header{upstreamHeaders, msg.Addon, msg.Headers, safeHeaders},
			"Retry-After",
		)),
		"upstream_request_id": sanitizeClaudeBootstrapDiagnosticValue(firstClaudeBootstrapDiagnosticHeader(
			[]http.Header{upstreamHeaders, msg.Addon, msg.Headers},
			"X-Upstream-Request-Id", "X-Request-Id", "Openai-Request-Id", "Request-Id", "Cf-Ray",
		)),
	}
	return fields, true
}

func claudeBootstrapErrorHasHeaders(err error) bool {
	if err == nil {
		return false
	}
	var headerErr interface{ Headers() http.Header }
	return errors.As(err, &headerErr) && headerErr != nil
}

func firstClaudeBootstrapDiagnosticHeader(headers []http.Header, names ...string) string {
	for _, header := range headers {
		for _, name := range names {
			if value := strings.TrimSpace(header.Get(name)); value != "" {
				return value
			}
		}
	}
	return ""
}

func sanitizeClaudeBootstrapDiagnosticValue(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > claudeStreamBootstrapDiagnosticValueLimit {
		value = string(runes[:claudeStreamBootstrapDiagnosticValueLimit]) + "…"
	}
	return value
}
