package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

const maxClaudeUpstreamDetailLen = 360

func isEbayClaudeGatewayBaseURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return isEbayClaudeGatewayURL(parsed)
}

func isEbayClaudeGatewayURL(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return strings.HasPrefix(host, "hubgptgateway") && strings.HasSuffix(host, ".ebay.com")
}

func normalizeClaudeUpstreamHTTPStatusError(model string, upstreamStatus int, body []byte) error {
	if upstreamStatus < http.StatusInternalServerError && upstreamStatus != 529 {
		return statusErr{code: upstreamStatus, msg: string(body)}
	}

	detail := summarizeClaudeUpstreamDetail(upstreamStatus, body)
	if upstreamStatus == http.StatusGatewayTimeout || strings.Contains(strings.ToLower(detail), "timeout") {
		return claudeUpstreamStatusErr(http.StatusGatewayTimeout, "timeout_error", claudeUpstreamTimeoutMessage(model, detail))
	}
	return claudeUpstreamStatusErr(http.StatusServiceUnavailable, "overloaded_error", claudeUpstreamCapacityMessage(model, detail))
}

func normalizeClaudeUpstreamTransportError(model string, err error) error {
	if err == nil {
		return nil
	}
	detail := sanitizeClaudeUpstreamDetail(err.Error())
	lower := strings.ToLower(err.Error())
	if isClaudeTimeoutError(err, lower) {
		return claudeUpstreamStatusErr(http.StatusGatewayTimeout, "timeout_error", claudeUpstreamTimeoutMessage(model, detail))
	}
	if isClaudeCapacityErrorText(lower) {
		return claudeUpstreamStatusErr(http.StatusServiceUnavailable, "overloaded_error", claudeUpstreamCapacityMessage(model, detail))
	}
	return claudeUpstreamStatusErr(http.StatusBadGateway, "api_error", claudeUpstreamConnectionMessage(model, detail))
}

func normalizeClaudeUpstreamSSEError(model string, payload []byte, partialOutput bool) error {
	detail := summarizeClaudeUpstreamDetail(http.StatusBadGateway, payload)
	if partialOutput {
		return claudeUpstreamStreamInterruptedError(model, detail, true)
	}

	errType := strings.TrimSpace(gjson.GetBytes(payload, "error.type").String())
	if errType == "" {
		errType = strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	}
	lower := strings.ToLower(detail + " " + errType)
	if strings.Contains(lower, "timeout") {
		return claudeUpstreamStatusErr(http.StatusGatewayTimeout, "timeout_error", claudeUpstreamTimeoutMessage(model, detail))
	}
	if errType == "overloaded_error" || isClaudeCapacityErrorText(lower) {
		return claudeUpstreamStatusErr(http.StatusServiceUnavailable, "overloaded_error", claudeUpstreamCapacityMessage(model, detail))
	}
	return claudeUpstreamStatusErr(http.StatusBadGateway, "api_error", claudeUpstreamConnectionMessage(model, detail))
}

func claudeUpstreamStreamInterruptedError(model, detail string, partialOutput bool) error {
	if strings.TrimSpace(detail) == "" {
		detail = "upstream stream ended before message_stop"
	}
	partialText := "No partial output was delivered; retry this turn."
	if partialOutput {
		partialText = "Partial output was already delivered, so CLIProxyAPI cannot safely replay transparently; retry this turn."
	}
	message := fmt.Sprintf("[upstream_stream_interrupted] Model %s reached eBay Claude Gateway, but the upstream stream ended unexpectedly. CLIProxyAPI accepted the request locally. %s Upstream detail: %s", claudeUpstreamModelText(model), partialText, sanitizeClaudeUpstreamDetail(detail))
	return claudeUpstreamStatusErr(http.StatusBadGateway, "api_error", message)
}

func claudeUpstreamCapacityMessage(model, detail string) string {
	return fmt.Sprintf("[upstream_capacity_unavailable] Model %s reached eBay Claude Gateway, but the upstream is temporarily busy or unavailable. CLIProxyAPI accepted the request locally; retry this turn later. Upstream detail: %s", claudeUpstreamModelText(model), sanitizeClaudeUpstreamDetail(detail))
}

func claudeUpstreamTimeoutMessage(model, detail string) string {
	return fmt.Sprintf("[upstream_timeout] Model %s reached eBay Claude Gateway, but the upstream timed out before completing. CLIProxyAPI accepted the request locally; retry this turn later. Upstream detail: %s", claudeUpstreamModelText(model), sanitizeClaudeUpstreamDetail(detail))
}

func claudeUpstreamConnectionMessage(model, detail string) string {
	return fmt.Sprintf("[upstream_connection_failed] Model %s reached eBay Claude Gateway, but the upstream connection failed before a complete response. CLIProxyAPI accepted the request locally; retry this turn later. Upstream detail: %s", claudeUpstreamModelText(model), sanitizeClaudeUpstreamDetail(detail))
}

func claudeUpstreamModelText(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

func claudeUpstreamStatusErr(status int, errType, message string) error {
	payload := map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return statusErr{code: status, msg: message}
	}
	return statusErr{code: status, msg: string(data)}
}

func summarizeClaudeUpstreamDetail(status int, body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return http.StatusText(status)
	}
	if gjson.ValidBytes(trimmed) {
		parts := make([]string, 0, 3)
		for _, path := range []string{"error.type", "error.code", "error.message", "type", "code", "message"} {
			value := strings.TrimSpace(gjson.GetBytes(trimmed, path).String())
			if value != "" {
				parts = append(parts, value)
			}
		}
		if len(parts) > 0 {
			return sanitizeClaudeUpstreamDetail(strings.Join(parts, ": "))
		}
	}
	return sanitizeClaudeUpstreamDetail(string(trimmed))
}

func sanitizeClaudeUpstreamDetail(detail string) string {
	detail = strings.TrimSpace(strings.Join(strings.Fields(detail), " "))
	if detail == "" {
		return "no upstream detail"
	}
	lower := strings.ToLower(detail)
	for _, marker := range []string{"authorization:", "authorization=", "bearer ", "x-api-key", "api_key", "access_token", "refresh_token", "id_token", "sk-"} {
		if strings.Contains(lower, marker) {
			return "redacted upstream detail"
		}
	}
	if len(detail) <= maxClaudeUpstreamDetailLen {
		return detail
	}
	return strings.TrimSpace(detail[:maxClaudeUpstreamDetailLen]) + "..."
}

func isClaudeTimeoutError(err error, lower string) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded")
}

func isClaudeCapacityErrorText(lower string) bool {
	for _, marker := range []string{"overloaded", "capacity", "temporarily unavailable", "server busy", "too busy", "try again later", "no healthy upstream"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func claudeSSEDataPayload(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	return payload, true
}

func claudeSSELineType(line []byte) string {
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return strings.TrimSpace(string(bytes.TrimSpace(trimmed[len("event:"):])))
	}
	payload, ok := claudeSSEDataPayload(trimmed)
	if !ok || !gjson.ValidBytes(payload) {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "type").String())
}

func claudeSSEErrorPayload(line []byte) ([]byte, bool) {
	payload, ok := claudeSSEDataPayload(line)
	if !ok || !gjson.ValidBytes(payload) {
		return nil, false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil, false
	}
	return payload, true
}
