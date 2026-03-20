package upstreamerrors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Normalized struct {
	Status  int
	Message string
	Type    string
	Code    string
}

func Normalize(status int, errText string) Normalized {
	if status <= 0 {
		status = http.StatusInternalServerError
	}

	normalized := Normalized{Status: status}
	trimmed := strings.TrimSpace(errText)
	if trimmed != "" {
		normalized.Message = trimmed
	}
	defaultType, defaultCode := defaultTypeAndCode(status)
	normalized.Type = defaultType
	normalized.Code = defaultCode

	var kind string
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			kind = strings.TrimSpace(stringValue(payload["kind"]))
			applyTopLevelPayload(&normalized, payload)
			if errorPayload, ok := payload["error"].(map[string]any); ok {
				applyErrorPayload(&normalized, errorPayload)
			}
		}
	}

	applyKnownUpstreamHeuristics(&normalized, kind)
	if strings.TrimSpace(normalized.Message) == "" {
		normalized.Message = strings.TrimSpace(http.StatusText(normalized.Status))
	}
	if normalized.Message == "" {
		normalized.Message = "Internal Server Error"
	}
	if strings.TrimSpace(normalized.Type) == "" {
		normalized.Type, _ = defaultTypeAndCode(normalized.Status)
	}
	if strings.TrimSpace(normalized.Code) == "" {
		_, normalized.Code = defaultTypeAndCode(normalized.Status)
	}
	return normalized
}

func IsRequestInvalid(status int, errText string) bool {
	normalized := Normalize(status, errText)
	switch normalized.Status {
	case http.StatusBadRequest:
		return strings.EqualFold(strings.TrimSpace(normalized.Type), "invalid_request_error")
	case http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func applyTopLevelPayload(normalized *Normalized, payload map[string]any) {
	if normalized == nil || payload == nil {
		return
	}
	if message := strings.TrimSpace(stringValue(payload["message"])); message != "" {
		normalized.Message = message
	}
	if code := strings.TrimSpace(stringValue(payload["code"])); code != "" {
		normalized.Code = code
	}
	if rawType := strings.TrimSpace(stringValue(payload["type"])); rawType != "" && !strings.EqualFold(rawType, "error") {
		normalized.Type = rawType
	}
}

func applyErrorPayload(normalized *Normalized, payload map[string]any) {
	if normalized == nil || payload == nil {
		return
	}
	if message := strings.TrimSpace(stringValue(payload["message"])); message != "" {
		normalized.Message = message
	}
	if rawType := strings.TrimSpace(stringValue(payload["type"])); rawType != "" {
		normalized.Type = rawType
	}
	if code := strings.TrimSpace(stringValue(payload["code"])); code != "" {
		normalized.Code = code
	}
}

func applyKnownUpstreamHeuristics(normalized *Normalized, kind string) {
	if normalized == nil {
		return
	}
	lowerKind := strings.ToLower(strings.TrimSpace(kind))
	lowerMessage := strings.ToLower(strings.TrimSpace(normalized.Message))
	if lowerKind == "request_error:request_body_truncated" || strings.Contains(lowerMessage, "context canceled") {
		normalized.Status = http.StatusBadGateway
		normalized.Message = "upstream request was interrupted before completion"
		normalized.Type = "server_error"
		normalized.Code = "upstream_request_interrupted"
	}
}

func defaultTypeAndCode(status int) (string, string) {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error", "invalid_api_key"
	case http.StatusForbidden:
		return "permission_error", "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_error", "rate_limit_exceeded"
	case http.StatusNotFound:
		return "invalid_request_error", "model_not_found"
	default:
		if status >= http.StatusInternalServerError {
			return "server_error", "internal_server_error"
		}
		return "invalid_request_error", ""
	}
}

func stringValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}
