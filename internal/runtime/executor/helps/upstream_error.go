package helps

import (
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// UpstreamBodyError is a StatusError surfaced when an upstream provider returns
// an HTTP 2xx response whose JSON body nevertheless carries an error object
// (e.g. {"error":{"message":"Insufficient quota.","type":"insufficient_quota"}}).
// Some OpenAI-compatible gateways report quota/billing failures this way instead
// of using a proper non-2xx status code, which would otherwise cause the proxy
// to forward the error payload to the client as a successful response and skip
// any retry across alternative credentials.
type UpstreamBodyError struct {
	Code       int
	Message    string
	RetryAfter *time.Duration
}

func (e UpstreamBodyError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return http.StatusText(e.Code)
}

// StatusCode implements cliproxyexecutor.StatusError so the auth conductor can
// classify the failure and trigger credential rotation / cooldown.
func (e UpstreamBodyError) StatusCode() int { return e.Code }

// RetryAfterDuration exposes an optional cooldown hint, mirroring the statusErr
// contract used by provider executors.
func (e UpstreamBodyError) RetryAfterDuration() *time.Duration { return e.RetryAfter }

// DetectUpstreamErrorBody inspects an upstream JSON response body that was
// received with an HTTP 2xx status and returns an UpstreamBodyError when the
// payload carries a top-level "error" object. The HTTP status passed in is the
// one actually received from upstream (typically 200); the returned error's
// StatusCode is derived from the error type/code so the retry classifier can
// treat quota/billing failures as retryable across credentials.
//
// Returns nil when the body is not valid JSON or does not contain an error
// object, in which case the caller should treat the response as successful.
func DetectUpstreamErrorBody(httpStatus int, body []byte) *UpstreamBodyError {
	if len(body) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil
	}
	errField := gjson.Get(trimmed, "error")
	if !errField.Exists() {
		return nil
	}
	if errField.Type == gjson.String {
		msg := strings.TrimSpace(errField.String())
		if msg == "" {
			return nil
		}
		return &UpstreamBodyError{
			Code:    inferUpstreamErrorStatus(httpStatus, "", msg),
			Message: string(body),
		}
	}
	if errField.Type != gjson.JSON {
		return nil
	}

	errType := strings.ToLower(strings.TrimSpace(errField.Get("type").String()))
	errCode := strings.ToLower(strings.TrimSpace(errField.Get("code").String()))
	message := strings.TrimSpace(errField.Get("message").String())
	if errType == "" && errCode == "" && message == "" {
		return nil
	}

	status := inferUpstreamErrorStatus(httpStatus, errType+" "+errCode, message)
	return &UpstreamBodyError{
		Code:    status,
		Message: string(body),
	}
}

// inferUpstreamErrorStatus maps known upstream error type/code strings to an
// HTTP-like status code that the auth conductor understands for cooldown and
// retry decisions. Unknown error shapes fall back to the received HTTP status,
// or 502 Bad Gateway when the upstream masked an error behind HTTP 200.
func inferUpstreamErrorStatus(httpStatus int, typeOrCode string, message string) int {
	lower := strings.ToLower(typeOrCode + " " + message)
	switch {
	case strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "billing") ||
		strings.Contains(lower, "payment") ||
		strings.Contains(lower, "credit") ||
		strings.Contains(lower, "insufficient"):
		return http.StatusPaymentRequired
	case strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "usage_limit") ||
		strings.Contains(lower, "capacity"):
		return http.StatusTooManyRequests
	case strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid_api_key") ||
		strings.Contains(lower, "authentication"):
		return http.StatusUnauthorized
	case strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "permission"):
		return http.StatusForbidden
	case strings.Contains(lower, "not_found") ||
		strings.Contains(lower, "model_not_found"):
		return http.StatusNotFound
	}
	if httpStatus >= 200 && httpStatus < 300 {
		return http.StatusBadGateway
	}
	return httpStatus
}
