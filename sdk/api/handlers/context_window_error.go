package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	contextWindowExceededErrorCode = "context_window_exceeded"
	contextWindowExceededErrorType = "context_length_exceeded"

	requestedModelContextKey = "__requested_model__"
	selectedAuthIDContextKey = "__selected_auth_id__"
)

// UserFacingContextWindowMessage returns the normalized client-facing message for context-limit failures.
func UserFacingContextWindowMessage() string {
	return "当前对话上下文已超过模型限制。请清理或压缩历史消息，或新建对话后重试。"
}

// IsContextWindowExceededError reports whether the upstream error matches a context-limit rejection.
func IsContextWindowExceededError(status int, errText string) bool {
	if status > 0 && status < http.StatusBadRequest {
		return false
	}
	for _, candidate := range contextWindowErrorCandidates(errText) {
		if hasContextWindowExceededSignal(candidate) {
			return true
		}
	}
	return false
}

// BuildContextWindowExceededErrorBody builds a normalized OpenAI-style error body for context-limit failures.
func BuildContextWindowExceededErrorBody(status int, errText string) ([]byte, bool) {
	detail, ok := contextWindowExceededErrorDetail(status, errText)
	if !ok {
		return nil, false
	}
	payload, err := json.Marshal(ErrorResponse{Error: detail})
	if err != nil {
		return []byte(`{"error":{"message":"context window exceeded","type":"context_length_exceeded","code":"context_window_exceeded"}}`), true
	}
	return payload, true
}

func contextWindowExceededErrorDetail(status int, errText string) (ErrorDetail, bool) {
	if !IsContextWindowExceededError(status, errText) {
		return ErrorDetail{}, false
	}
	return ErrorDetail{
		Message: UserFacingContextWindowMessage(),
		Type:    contextWindowExceededErrorType,
		Code:    contextWindowExceededErrorCode,
	}, true
}

func contextWindowErrorCandidates(errText string) []string {
	trimmed := strings.TrimSpace(errText)
	if trimmed == "" {
		return nil
	}

	candidates := []string{trimmed}
	if !json.Valid([]byte(trimmed)) {
		return candidates
	}

	for _, path := range []string{
		"error.message",
		"error.code",
		"error.type",
		"message",
		"detail",
		"code",
		"type",
	} {
		value := strings.TrimSpace(gjson.Get(trimmed, path).String())
		if value != "" {
			candidates = append(candidates, value)
		}
	}
	return candidates
}

func hasContextWindowExceededSignal(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}

	switch lower {
	case contextWindowExceededErrorCode, "context_length_exceeded", "context_too_large", "model_context_window_exceeded":
		return true
	}

	patterns := []string{
		"context window exceeds limit",
		"context window exceeded",
		"context length exceeded",
		"context length exceeds",
		"maximum context length",
		"max context length",
		"input exceeds context",
		"prompt is too long",
		"context limit",
		"exceeds the model's context",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	if strings.Contains(lower, "tokens exceed") && strings.Contains(lower, "context") {
		return true
	}
	if strings.Contains(lower, "too many tokens") && (strings.Contains(lower, "context") || strings.Contains(lower, "prompt")) {
		return true
	}
	if strings.Contains(lower, "(2013)") && strings.Contains(lower, "context") &&
		(strings.Contains(lower, "window") || strings.Contains(lower, "length") || strings.Contains(lower, "limit")) {
		return true
	}
	if strings.Contains(lower, "2013") && strings.Contains(lower, "context") && strings.Contains(lower, "limit") {
		return true
	}

	return false
}

func recordRequestedModelContext(ctx context.Context, modelName string) {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return
	}
	ginCtx.Set(requestedModelContextKey, modelName)
}

func attachSelectedAuthTrackingCallback(ctx context.Context, meta map[string]any) {
	if meta == nil {
		return
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return
	}

	original, _ := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	meta[coreexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		authID = strings.TrimSpace(authID)
		if authID != "" {
			ginCtx.Set(selectedAuthIDContextKey, authID)
		}
		if original != nil {
			original(authID)
		}
	}
}

// LogContextWindowExceededEvent emits a structured log entry when a context-limit rejection is returned to clients.
func LogContextWindowExceededEvent(c *gin.Context, status int, errText string, authManager *coreauth.Manager) {
	if c == nil || !IsContextWindowExceededError(status, errText) {
		return
	}

	fields := log.Fields{
		"event":       contextWindowExceededErrorCode,
		"status_code": status,
	}

	if requestID := strings.TrimSpace(logging.GetGinRequestID(c)); requestID != "" {
		fields["request_id"] = requestID
	} else if c.Request != nil {
		if requestID := strings.TrimSpace(logging.GetRequestID(c.Request.Context())); requestID != "" {
			fields["request_id"] = requestID
		}
	}

	if requestPath := strings.TrimSpace(c.FullPath()); requestPath != "" {
		fields["request_path"] = requestPath
	} else if c.Request != nil && c.Request.URL != nil {
		if requestPath := strings.TrimSpace(c.Request.URL.Path); requestPath != "" {
			fields["request_path"] = requestPath
		}
	}

	if requestedModel, exists := c.Get(requestedModelContextKey); exists {
		if value, ok := requestedModel.(string); ok && strings.TrimSpace(value) != "" {
			value = strings.TrimSpace(value)
			fields["requested_model"] = value
		}
	}

	if upstreamModel := contextWindowUpstreamModelFromAPIRequest(c); upstreamModel != "" {
		fields["upstream_model"] = upstreamModel
	}

	if authManager != nil {
		if selectedAuthID, exists := c.Get(selectedAuthIDContextKey); exists {
			if authID, ok := selectedAuthID.(string); ok && strings.TrimSpace(authID) != "" {
				authID = strings.TrimSpace(authID)
				if auth, ok := authManager.GetByID(authID); ok && auth != nil {
					if provider := strings.TrimSpace(auth.Provider); provider != "" {
						fields["provider"] = provider
					}
					providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
					if providerKey == "" {
						providerKey = strings.TrimSpace(auth.Provider)
					}
					if providerKey != "" {
						fields["provider_key"] = providerKey
					}
				}
			}
		}
	}

	log.WithFields(fields).Warn("context window exceeded")
}

func contextWindowUpstreamModelFromAPIRequest(c *gin.Context) string {
	if c == nil {
		return ""
	}
	rawValue, exists := c.Get("API_REQUEST")
	if !exists {
		return ""
	}
	raw, ok := rawValue.([]byte)
	if !ok || len(raw) == 0 {
		return ""
	}

	segment := raw
	if idx := bytes.LastIndex(raw, []byte("\nBody:\n")); idx >= 0 {
		segment = raw[idx+len("\nBody:\n"):]
	}
	if idx := bytes.Index(segment, []byte("\n\n")); idx >= 0 {
		segment = segment[:idx]
	}
	segment = bytes.TrimSpace(segment)
	if len(segment) == 0 || !json.Valid(segment) {
		return ""
	}

	return strings.TrimSpace(gjson.GetBytes(segment, "model").String())
}
