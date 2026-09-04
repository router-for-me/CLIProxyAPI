// Package clienterror classifies upstream failures caused by the client request.
package clienterror

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// StatusClientClosedRequest is the nginx-style status used when the client
// aborts the request before the proxy finishes (context.Canceled).
const StatusClientClosedRequest = 499

var requestFaultCodes = map[string]struct{}{
	"cyber_policy":                {},
	"context_length_exceeded":     {},
	"message_too_big":             {},
	"string_above_max_length":     {},
	"invalid_prompt":              {},
	"invalid_value":               {},
	"unsupported_value":           {},
	"invalid_request_error":       {},
	"previous_response_not_found": {},
}

var requestFaultTypes = map[string]struct{}{
	"invalid_request":       {},
	"invalid_request_error": {},
	"bad_request_error":     {},
	"invalid_prompt":        {},
}

const (
	outOfExtraUsageNeedle       = "out of extra usage"
	outOfExtraUsageExactMessage = "You're out of extra usage. Add more at claude.ai/settings/usage and keep going."
)

// HTTPStatusFromError extracts an HTTP status from err.
// Explicit StatusCode() values win. Otherwise context.Canceled maps to 499
// and context.DeadlineExceeded maps to 504. Returns 0 when unknown.
func HTTPStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		if code := sc.StatusCode(); code > 0 {
			return code
		}
	}
	if errors.Is(err, context.Canceled) {
		return StatusClientClosedRequest
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return 0
}

// HTTPStatusFromErrorOr is like HTTPStatusFromError but returns fallback when
// the error does not carry a known status.
func HTTPStatusFromErrorOr(err error, fallback int) int {
	if code := HTTPStatusFromError(err); code > 0 {
		return code
	}
	return fallback
}

// IsRequestFault reports whether an upstream failure is caused by the request
// and therefore must not rotate or penalize credentials.
func IsRequestFault(status int, err error) bool {
	if status <= 0 && err != nil {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if errors.As(err, &statusErr) && statusErr != nil {
			status = statusErr.StatusCode()
		}
	}
	// Payment and rate-limit statuses are authoritative even when an upstream
	// pairs them with a generic invalid_request_error body. The credential must
	// remain eligible for cooldown and rotation.
	if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
		return false
	}
	// Anthropic reports extra-usage exhaustion as HTTP 400 invalid_request_error
	// rather than 429. Keep that credential eligible for cooldown and rotation
	// without treating every account-level 400 as quota.
	if IsOutOfExtraUsage(status, err) {
		return false
	}
	// DeepSeek reports an invalid API key as 401 with the authentication_error
	// type alongside the same generic code. Preserve that credential failure
	// classification without weakening generic request-fault handling.
	if status == http.StatusUnauthorized && hasAuthenticationErrorBody(err) {
		return false
	}
	if hasRequestFaultBody(err) {
		return true
	}
	if err != nil && IsItemNotPersisted(err.Error()) {
		return true
	}
	switch status {
	case http.StatusBadRequest,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// IsOutOfExtraUsage reports Anthropic's extra-usage exhaustion. Claude delivers
// it as HTTP 400 invalid_request_error, not 429. The needle is the phrase
// "out of extra usage" (covering you're-less variants); generic
// invalid_request_error 400s stay request faults.
func IsOutOfExtraUsage(status int, err error) bool {
	if err == nil {
		return false
	}
	if status <= 0 {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if errors.As(err, &statusErr) && statusErr != nil {
			status = statusErr.StatusCode()
		}
	}
	if status != http.StatusBadRequest {
		return false
	}
	return isOutOfExtraUsageBody(errorBody(err))
}

func isOutOfExtraUsageBody(body string) bool {
	body = strings.TrimSpace(body)
	if body == "" {
		return false
	}
	if body == outOfExtraUsageExactMessage {
		return true
	}
	message := body
	hasInvalidRequestType := false
	if json.Valid([]byte(body)) {
		for _, path := range []string{"error.message", "message", "response.error.message", "body.error.message"} {
			if value := strings.TrimSpace(gjson.Get(body, path).String()); value != "" {
				message = value
				break
			}
		}
		for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
			if value := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())); value == "invalid_request_error" {
				hasInvalidRequestType = true
				break
			}
		}
	}
	if message == outOfExtraUsageExactMessage {
		return true
	}
	if !strings.Contains(strings.ToLower(message), outOfExtraUsageNeedle) &&
		!strings.Contains(strings.ToLower(body), outOfExtraUsageNeedle) {
		return false
	}
	if hasInvalidRequestType {
		return true
	}
	// Non-JSON wrappers still need the Anthropic type token so a random 400
	// that merely mentions extra usage is not treated as quota.
	return !json.Valid([]byte(body)) && strings.Contains(strings.ToLower(body), "invalid_request_error")
}

func errorBody(err error) string {
	if err == nil {
		return ""
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var bodyProvider responseBodyProvider
	if errors.As(err, &bodyProvider) && bodyProvider != nil {
		if body := bodyProvider.ResponseBody(); len(body) > 0 {
			return string(body)
		}
	}
	return err.Error()
}

// IsItemNotPersisted matches the upstream 404 raised when a request references a
// response item the upstream never stored because `store` was false. The upstream
// sends this as a plain-text message rather than a JSON body, so it cannot be
// recognized through the structured identifiers above.
//
// The request can only succeed once the client rebuilds it without the stale
// reference, so it is a request fault: rotating credentials cannot help, and the
// client must be told rather than left to retry the same broken input.
func IsItemNotPersisted(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

func hasAuthenticationErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(errorBody(err))
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		if errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())); errType == "authentication_error" {
			return true
		}
	}
	return false
}

func hasRequestFaultBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(errorBody(err))
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if _, ok := requestFaultCodes[code]; ok {
			return true
		}
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if _, ok := requestFaultTypes[errType]; ok {
			return true
		}
	}
	return false
}

// IsClientCancellation reports whether an HTTP status code or error represents
// a client-initiated cancellation (HTTP 499 StatusClientClosedRequest or context.Canceled).
func IsClientCancellation(status int, err error) bool {
	if status == StatusClientClosedRequest {
		return true
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return true
		}
		type statusCoder interface {
			StatusCode() int
		}
		var sc statusCoder
		if errors.As(err, &sc) && sc != nil && sc.StatusCode() == StatusClientClosedRequest {
			return true
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "context canceled") || strings.Contains(lower, "client closed request") {
			return true
		}
	}
	return false
}
