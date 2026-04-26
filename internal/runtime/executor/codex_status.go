package executor

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var codexOrdinalDaySuffixRE = regexp.MustCompile(`\b(\d{1,2})(st|nd|rd|th)\b`)

func newCodexStatusErr(statusCode int, body []byte) statusErr {
	errCode := codexStatusCode(statusCode, body)
	if errCode <= 0 {
		errCode = http.StatusInternalServerError
	}
	err := statusErr{code: errCode, msg: string(body)}
	if retryAfter := parseCodexRetryAfter(errCode, body, time.Now()); retryAfter != nil {
		err.retryAfter = retryAfter
	}
	return err
}

func isCodexModelCapacityError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "selected model is at capacity") ||
			strings.Contains(lower, "model is at capacity. please try a different model") {
			return true
		}
	}
	return false
}

func isCodexUsageLimitError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(errorBody, "error.type").String()), "usage_limit_reached") {
		return true
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.code").String(),
		gjson.GetBytes(errorBody, "code").String(),
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "usage_limit_reached") ||
			strings.Contains(lower, "you've hit your usage limit") ||
			strings.Contains(lower, "upgrade to plus") ||
			strings.Contains(lower, "continue using codex") {
			return true
		}
	}
	return false
}

func codexStatusCode(statusCode int, body []byte) int {
	if isCodexUsageLimitError(body) || isCodexModelCapacityError(body) {
		return http.StatusTooManyRequests
	}
	return statusCode
}

func parseCodexRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if !isCodexUsageLimitError(errorBody) {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	if resetsInSeconds := gjson.GetBytes(errorBody, "error.resets_in_seconds").Int(); resetsInSeconds > 0 {
		retryAfter := time.Duration(resetsInSeconds) * time.Second
		return &retryAfter
	}
	if retryAfter := parseCodexRetryAfterMessage(errorBody, now); retryAfter != nil {
		return retryAfter
	}
	return nil
}

func parseCodexRetryAfterMessage(errorBody []byte, now time.Time) *time.Duration {
	candidates := []string{
		gjson.GetBytes(errorBody, "error.retry_at").String(),
		gjson.GetBytes(errorBody, "error.try_again_at").String(),
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
	}
	for _, candidate := range candidates {
		if retryAfter := parseCodexRetryAfterCandidate(candidate, now); retryAfter != nil {
			return retryAfter
		}
	}
	return nil
}

func parseCodexRetryAfterCandidate(candidate string, now time.Time) *time.Duration {
	for _, retryAt := range codexRetryAtCandidates(candidate, now.Location()) {
		retryAfter := retryAt.Sub(now)
		if retryAfter > 0 {
			return &retryAfter
		}
	}
	return nil
}

func codexRetryAtCandidates(candidate string, loc *time.Location) []time.Time {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return nil
	}
	normalized := codexOrdinalDaySuffixRE.ReplaceAllString(candidate, `$1`)
	candidates := []string{strings.TrimSpace(strings.Trim(normalized, `"'`))}
	lower := strings.ToLower(normalized)
	if idx := strings.Index(lower, "try again at "); idx >= 0 {
		candidates = append(candidates, strings.TrimSpace(normalized[idx+len("try again at "):]))
	}

	out := make([]time.Time, 0, len(candidates))
	for _, value := range candidates {
		value = strings.TrimSpace(strings.TrimSuffix(strings.Trim(value, `"'`), "."))
		if value == "" {
			continue
		}
		if retryAt, ok := parseCodexRetryAtTime(value, loc); ok {
			out = append(out, retryAt)
		}
	}
	return out
}

func parseCodexRetryAtTime(value string, loc *time.Location) (time.Time, bool) {
	layoutsWithLocation := []string{
		"January 2, 2006 3:04:05 PM",
		"January 2, 2006 3:04 PM",
		"Jan 2, 2006 3:04:05 PM",
		"Jan 2, 2006 3:04 PM",
	}
	for _, layout := range layoutsWithLocation {
		if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
			return parsed, true
		}
	}

	layouts := []string{
		"January 2, 2006 3:04:05 PM MST",
		"January 2, 2006 3:04 PM MST",
		"Jan 2, 2006 3:04:05 PM MST",
		"Jan 2, 2006 3:04 PM MST",
		time.RFC3339,
		time.RFC1123,
		time.RFC1123Z,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func parseCodexStreamTerminalError(eventType string, eventData []byte) (statusErr, bool) {
	switch strings.TrimSpace(eventType) {
	case "error":
		err, ok := parseCodexWebsocketError(eventData)
		if !ok || err == nil {
			return statusErr{}, false
		}
		if withHeaders, ok := err.(statusErrWithHeaders); ok {
			return withHeaders.statusErr, true
		}
		if plain, ok := err.(statusErr); ok {
			return plain, true
		}
		return statusErr{}, false
	case "response.failed":
		body := normalizeCodexResponseFailedErrorBody(eventData)
		status := parseCodexResponseFailedStatus(eventData, body)
		if status <= 0 {
			status = http.StatusInternalServerError
		}
		return newCodexStatusErr(status, body), true
	default:
		return statusErr{}, false
	}
}

func normalizeCodexResponseFailedErrorBody(eventData []byte) []byte {
	out := []byte(`{}`)
	errNode := gjson.GetBytes(eventData, "response.error")
	switch {
	case errNode.Exists() && errNode.IsObject():
		out, _ = sjson.SetRawBytes(out, "error", []byte(errNode.Raw))
	case errNode.Exists() && errNode.Type == gjson.String:
		out, _ = sjson.SetBytes(out, "error.message", strings.TrimSpace(errNode.String()))
	case errNode.Exists():
		out, _ = sjson.SetBytes(out, "error.message", strings.TrimSpace(errNode.Raw))
	}

	if strings.TrimSpace(gjson.GetBytes(out, "error.type").String()) == "" {
		switch {
		case isCodexUsageLimitError(out):
			out, _ = sjson.SetBytes(out, "error.type", "usage_limit_reached")
		default:
			out, _ = sjson.SetBytes(out, "error.type", "server_error")
		}
	}
	if strings.TrimSpace(gjson.GetBytes(out, "error.message").String()) == "" {
		out, _ = sjson.SetBytes(out, "error.message", "response.failed")
	}
	return out
}

func parseCodexResponseFailedStatus(eventData []byte, normalizedBody []byte) int {
	for _, path := range []string{"response.status", "response.status_code", "response.error.status", "response.error.status_code"} {
		if status := int(gjson.GetBytes(eventData, path).Int()); status > 0 {
			return codexStatusCode(status, normalizedBody)
		}
	}
	return codexStatusCode(0, normalizedBody)
}
