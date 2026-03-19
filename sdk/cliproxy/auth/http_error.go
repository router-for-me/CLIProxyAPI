package auth

import (
	"net/http"
	"strings"
)

// HTTPResponseError preserves an upstream HTTP failure for caller-side handling.
type HTTPResponseError struct {
	HTTPStatus int
	Body       []byte
	HeaderMap  http.Header
}

// Error implements the error interface.
func (e *HTTPResponseError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(string(e.Body))
	if message == "" {
		message = http.StatusText(e.HTTPStatus)
	}
	if message == "" {
		message = "upstream request failed"
	}
	return message
}

// StatusCode exposes the upstream HTTP status code.
func (e *HTTPResponseError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// Headers returns a defensive copy of the upstream response headers.
func (e *HTTPResponseError) Headers() http.Header {
	if e == nil || e.HeaderMap == nil {
		return nil
	}
	return e.HeaderMap.Clone()
}
