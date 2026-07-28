package handlers

import (
	"net/http"
	"testing"
)

type executionHeaderError struct {
	headers http.Header
}

func (e executionHeaderError) Error() string   { return "upstream unavailable" }
func (e executionHeaderError) StatusCode() int { return http.StatusServiceUnavailable }
func (e executionHeaderError) Headers() http.Header {
	return e.headers.Clone()
}

func TestExecutionErrorMessageFiltersUpstreamHeaders(t *testing.T) {
	err := executionHeaderError{headers: http.Header{
		"Retry-After":       {"120"},
		"X-Request-Id":      {"request-123"},
		"Content-Length":    {"999"},
		"Set-Cookie":        {"session=secret"},
		"Connection":        {"X-Connection-Only"},
		"X-Connection-Only": {"secret"},
		"X-Litellm-Trace":   {"gateway"},
	}}

	msg := executionErrorMessage(err)
	if msg == nil || msg.Error == nil || msg.Error.Error() != err.Error() {
		t.Fatalf("error message = %#v, want original error", msg)
	}
	if msg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", msg.StatusCode, http.StatusServiceUnavailable)
	}
	if got := msg.Addon.Get("Retry-After"); got != "120" {
		t.Fatalf("Retry-After = %q, want 120", got)
	}
	if got := msg.Addon.Get("X-Request-Id"); got != "request-123" {
		t.Fatalf("X-Request-Id = %q, want request-123", got)
	}
	for _, key := range []string{"Content-Length", "Set-Cookie", "Connection", "X-Connection-Only", "X-Litellm-Trace"} {
		if values := msg.Addon.Values(key); len(values) != 0 {
			t.Fatalf("%s leaked through filtered error headers: %v", key, values)
		}
	}
}
