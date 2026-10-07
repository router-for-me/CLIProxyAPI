// Package errorclass maps an upstream failure to a stable class slug. It is
// pure: no DB, no globals, no errors — an unparseable body falls through to the
// status/keyword arms and finally to ClassOther so classification can never
// fail a flush or drop a row.
package errorclass

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Stable class slugs. Display labels are resolved in the frontend so the
// persisted values never need to change when wording does.
const (
	ClassRateLimit      = "rate_limit"
	ClassAuth           = "auth"
	ClassInvalidRequest = "invalid_request"
	ClassNotFound       = "not_found"
	ClassPermission     = "permission"
	ClassTimeout        = "timeout"
	ClassConnection     = "connection"
	ClassServerError    = "server_error"
	ClassOverloaded     = "overloaded"
	ClassContentFilter  = "content_filter"
	ClassQuota          = "quota"
	ClassOther          = "other"
)

// Classify maps an upstream failure to a class slug. Resolution order is
// body (structured) → message keywords → status code → ClassOther. Keyword
// detection precedes the coarse status fallback so that a specific message
// (e.g. "upstream connection timeout" on a 5xx) wins over the generic class
// the status alone implies.
func Classify(statusCode int, body string) string {
	if c := classFromBody(body); c != "" {
		return c
	}
	if c := classFromKeywords(body); c != "" {
		return c
	}
	if c := classFromStatus(statusCode); c != "" {
		return c
	}
	return ClassOther
}

// classFromBody reads error.type / error.code from a JSON body. The mapping
// mirrors the precedent in internal/runtime/executor/codex_executor_terminal.go.
func classFromBody(body string) string {
	if strings.TrimSpace(body) == "" || !gjson.Valid(body) {
		return ""
	}
	t := strings.ToLower(strings.TrimSpace(gjson.Get(body, "error.type").String()))
	code := strings.ToLower(strings.TrimSpace(gjson.Get(body, "error.code").String()))
	switch {
	case t == "rate_limit_error" || code == "rate_limit_exceeded":
		return ClassRateLimit
	case code == "insufficient_quota":
		return ClassQuota
	case t == "authentication_error" || code == "invalid_api_key" || code == "unauthorized":
		return ClassAuth
	case t == "permission_error" || code == "forbidden" || code == "permission_denied":
		return ClassPermission
	case t == "not_found_error" || code == "not_found" || code == "model_not_found":
		return ClassNotFound
	case t == "invalid_request_error" || t == "bad_request_error":
		return ClassInvalidRequest
	case code == "content_filter" || t == "content_filter_error":
		return ClassContentFilter
	case t == "overloaded_error":
		return ClassOverloaded
	case t == "api_error" || t == "server_error":
		return ClassServerError
	}
	return ""
}

func classFromStatus(statusCode int) string {
	switch {
	case statusCode == 429:
		return ClassRateLimit
	case statusCode == 401:
		return ClassAuth
	case statusCode == 403:
		return ClassPermission
	case statusCode == 404:
		return ClassNotFound
	case statusCode == 400 || statusCode == 422:
		return ClassInvalidRequest
	case statusCode == 408 || statusCode == 504:
		return ClassTimeout
	case statusCode >= 500:
		return ClassServerError
	}
	return ""
}

func classFromKeywords(body string) string {
	s := strings.ToLower(body)
	switch {
	case strings.Contains(s, "context length") || strings.Contains(s, "context_length") || strings.Contains(s, "maximum context") || strings.Contains(s, "context window"):
		return ClassInvalidRequest
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return ClassTimeout
	case strings.Contains(s, "overloaded"):
		return ClassOverloaded
	case strings.Contains(s, "content filter") || strings.Contains(s, "content_filter"):
		return ClassContentFilter
	case strings.Contains(s, "quota"):
		return ClassQuota
	case strings.Contains(s, "rate limit") || strings.Contains(s, "rate_limit"):
		return ClassRateLimit
	case strings.Contains(s, "connection refused") || strings.Contains(s, "connection reset"):
		return ClassConnection
	case strings.Contains(s, "unauthorized") || strings.Contains(s, "invalid api key"):
		return ClassAuth
	}
	return ""
}
