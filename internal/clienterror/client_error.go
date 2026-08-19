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
	// Authentication and invalid-or-expired-credential failures are caused by
	// the credential, not the request: they must remain eligible for rotation
	// by the shared mixed-auth loop even when the provider pairs them with a
	// generic invalid-request identifier in the body. This must be checked
	// before hasRequestFaultBody so the generic classifier cannot misfile them.
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && hasAuthenticationErrorBody(err) {
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

// hasAuthenticationErrorBody reports whether err is a structured credential
// failure: an authentication_error type, an invalid or expired API-key code,
// or a Gemini UNAUTHENTICATED status. These are credential faults, not request
// faults, so they must never be classified as request faults on 401/403.
func hasAuthenticationErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())) {
		case "invalid_api_key", "incorrect_api_key", "expired_api_key":
			return true
		}
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		if errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())); errType == "authentication_error" {
			return true
		}
	}
	for _, path := range []string{"error.status", "status", "response.error.status", "body.error.status"} {
		if status := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())); status == "unauthenticated" {
			return true
		}
	}
	return false
}

func hasRequestFaultBody(err error) bool {
	if err == nil {
		return false
	}
	return HasRequestFaultBodyString(err.Error())
}

// HasRequestFaultBodyString reports whether a raw error body carries a
// structured request-fault code or type. It classifies the body only, with no
// status-based inference.
func HasRequestFaultBodyString(raw string) bool {
	body := strings.TrimSpace(raw)
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
