package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type executionHeaderError struct {
	headers http.Header
}

func (e executionHeaderError) Error() string   { return "upstream unavailable" }
func (e executionHeaderError) StatusCode() int { return http.StatusServiceUnavailable }
func (e executionHeaderError) Headers() http.Header {
	return e.headers.Clone()
}

func newExecutionHeaderError() executionHeaderError {
	return executionHeaderError{headers: http.Header{
		"Retry-After":       {"120"},
		"X-Request-Id":      {"request-123"},
		"Content-Length":    {"999"},
		"Set-Cookie":        {"session=secret"},
		"Connection":        {"X-Connection-Only"},
		"X-Connection-Only": {"secret"},
		"X-Litellm-Trace":   {"gateway"},
	}}
}

func TestExecutionErrorMessageFiltersUpstreamHeaders(t *testing.T) {
	err := newExecutionHeaderError()

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

func TestExecutionErrorHeadersRespectPassthroughToggle(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: enabled}, nil)
			handler.WriteErrorResponse(ctx, executionErrorMessage(newExecutionHeaderError()))

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
			}
			wantRetryAfter := ""
			wantRequestID := ""
			if enabled {
				wantRetryAfter = "120"
				wantRequestID = "request-123"
			}
			if got := recorder.Header().Get("Retry-After"); got != wantRetryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, wantRetryAfter)
			}
			if got := recorder.Header().Get("X-Request-Id"); got != wantRequestID {
				t.Fatalf("X-Request-Id = %q, want %q", got, wantRequestID)
			}
			if got := recorder.Header().Get("Set-Cookie"); got != "" {
				t.Fatalf("Set-Cookie leaked: %q", got)
			}
			if got := recorder.Header().Get("Content-Length"); got == "999" {
				t.Fatalf("upstream Content-Length leaked: %q", got)
			}
		})
	}
}
