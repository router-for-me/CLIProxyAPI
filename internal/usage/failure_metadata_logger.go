package usage

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const failureMetadataMaxStringLen = 160

func init() {
	coreusage.RegisterPlugin(&FailureMetadataLogger{})
}

// FailureMetadataLogger emits a safe structured log for failed upstream attempts.
// It never logs request bodies, response bodies, auth IDs, API keys, headers, or raw error text.
type FailureMetadataLogger struct{}

// HandleUsage implements coreusage.Plugin.
func (p *FailureMetadataLogger) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || !record.Failed {
		return
	}

	attempt := coreusage.RequestAttemptFromContext(ctx)
	shape := coreusage.RequestShapeFromContext(ctx)
	messageCount := record.MessageCount
	if messageCount <= 0 {
		messageCount = shape.MessageCount
	}
	toolCount := record.ToolCount
	if toolCount <= 0 {
		toolCount = shape.ToolCount
	}
	attemptCount := record.AttemptNo
	if attemptCount <= 0 {
		attemptCount = attempt.AttemptNo
	}

	status := failureMetadataStatus(record)
	errorCode := safeFailureMetadataString(record.ErrorCode)
	if errorCode == "" {
		errorCode = safeFailureMetadataString(record.Fail.ErrorCode)
	}
	reasoningEffort := safeFailureMetadataString(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = safeFailureMetadataString(coreusage.ReasoningEffortFromContext(ctx))
	}

	model := safeFailureMetadataString(record.Alias)
	if model == "" {
		model = safeFailureMetadataString(record.Model)
	}

	fields := log.Fields{
		"event":            "failure_metadata",
		"failure_class":    classifyFailureMetadata(status, errorCode),
		"model":            model,
		"endpoint":         safeFailureMetadataString(internallogging.GetEndpoint(ctx)),
		"message_count":    messageCount,
		"tool_count":       toolCount,
		"reasoning_effort": reasoningEffort,
		"attempt_count":    attemptCount,
		"duration_ms":      durationMilliseconds(record.Latency),
	}
	if status > 0 {
		fields["upstream_status"] = status
	}
	if errorCode != "" {
		fields["upstream_error_code"] = errorCode
	}
	if requestID := safeFailureMetadataString(resolveFailureMetadataRequestID(ctx, record, attempt)); requestID != "" {
		fields["request_id"] = requestID
	}
	if authIndex := safeFailureMetadataString(record.AuthIndex); authIndex != "" {
		fields["auth_index"] = authIndex
	}
	if routingGroup := safeFailureMetadataString(coreusage.RoutingGroupFromContext(ctx)); routingGroup != "" {
		fields["routing_group"] = routingGroup
	}

	log.WithFields(fields).Warn("failure_metadata")
}

func failureMetadataStatus(record coreusage.Record) int {
	if record.ProviderStatusCode > 0 {
		return record.ProviderStatusCode
	}
	if record.Fail.StatusCode > 0 {
		return record.Fail.StatusCode
	}
	return 0
}

func resolveFailureMetadataRequestID(ctx context.Context, record coreusage.Record, attempt coreusage.RequestAttempt) string {
	if requestID := strings.TrimSpace(record.RequestID); requestID != "" {
		return requestID
	}
	if requestID := strings.TrimSpace(attempt.RequestID); requestID != "" {
		return requestID
	}
	return internallogging.GetRequestID(ctx)
}

func durationMilliseconds(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func classifyFailureMetadata(status int, code string) string {
	normalizedCode := strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.Contains(normalizedCode, "empty"):
		return "empty_response"
	case strings.Contains(normalizedCode, "transient_routing"):
		return "transient_routing"
	case strings.Contains(normalizedCode, "timeout") || strings.Contains(normalizedCode, "deadline"):
		return "timeout"
	case strings.Contains(normalizedCode, "rate_limit"):
		return "rate_limit"
	case strings.Contains(normalizedCode, "quota") || strings.Contains(normalizedCode, "insufficient_balance"):
		return "quota"
	case strings.Contains(normalizedCode, "unauthor") || strings.Contains(normalizedCode, "invalid_api_key"):
		return "auth"
	case strings.Contains(normalizedCode, "permission") || strings.Contains(normalizedCode, "forbidden"):
		return "permission"
	case strings.Contains(normalizedCode, "model_not") || strings.Contains(normalizedCode, "model_unsupported"):
		return "model_unavailable"
	case strings.Contains(normalizedCode, "api_error") || strings.Contains(normalizedCode, "internal_server_error"):
		return "upstream_api_error"
	}

	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status == http.StatusUnauthorized:
		return "auth"
	case status == http.StatusPaymentRequired:
		return "quota"
	case status == http.StatusForbidden:
		return "permission"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return "timeout"
	case status == http.StatusNotFound:
		return "model_unavailable"
	case status >= http.StatusInternalServerError:
		return "upstream_5xx"
	case status >= http.StatusBadRequest:
		return "request_4xx"
	default:
		return "unknown"
	}
}

func safeFailureMetadataString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if looksLikeFailureMetadataSecret(value) {
		return "[redacted]"
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if len(value) <= failureMetadataMaxStringLen {
		return value
	}
	return value[:failureMetadataMaxStringLen] + "...[truncated " + strconv.Itoa(len(value)-failureMetadataMaxStringLen) + " bytes]"
}

func looksLikeFailureMetadataSecret(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "bearer ") ||
		strings.HasPrefix(lower, "sk-") ||
		strings.HasPrefix(lower, "sk_") ||
		strings.Contains(lower, "api_key=") ||
		strings.Contains(lower, "apikey=") ||
		strings.Contains(lower, "authorization:")
}
