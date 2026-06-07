package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestWriteErrorResponse_AddonHeadersDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":  {"30"},
			"X-Request-Id": {"req-1"},
		},
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After should be empty when passthrough is disabled, got %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should be empty when passthrough is disabled, got %q", got)
	}
}

func TestWriteErrorResponse_AddonHeadersEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Writer.Header().Set("X-Request-Id", "old-value")

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":  {"30"},
			"X-Request-Id": {"new-1", "new-2"},
		},
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want %q", got, "30")
	}
	if got := recorder.Header().Values("X-Request-Id"); !reflect.DeepEqual(got, []string{"new-1", "new-2"}) {
		t.Fatalf("X-Request-Id = %#v, want %#v", got, []string{"new-1", "new-2"})
	}
}

func TestEnrichAuthSelectionError_DefaultsTo503WithConciseMessage(t *testing.T) {
	in := &coreauth.Error{Code: "auth_not_found", Message: "no auth available"}
	out := enrichAuthSelectionError(in, []string{"claude"}, "claude-sonnet-4-6")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusServiceUnavailable)
	}
	if got.Message != "requested route is temporarily unavailable" {
		t.Fatalf("message = %q, want %q", got.Message, "requested route is temporarily unavailable")
	}
}

func TestEnrichAuthSelectionError_PreservesExplicitStatus(t *testing.T) {
	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available", HTTPStatus: http.StatusTooManyRequests}
	out := enrichAuthSelectionError(in, []string{"gemini"}, "gemini-2.5-pro")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusTooManyRequests)
	}
}

func TestEnrichAuthSelectionError_IgnoresOtherErrors(t *testing.T) {
	in := errors.New("boom")
	out := enrichAuthSelectionError(in, []string{"claude"}, "claude-sonnet-4-6")
	if out != in {
		t.Fatalf("expected original error to be returned unchanged")
	}
}

func TestBuildErrorResponseBody_NormalizesContextWindowPlainText(t *testing.T) {
	body := BuildErrorResponseBody(http.StatusBadRequest, "bad_request_error: invalid params, context window exceeds limit (2013)")

	var payload ErrorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Error.Message != UserFacingContextWindowMessage() {
		t.Fatalf("message = %q, want %q", payload.Error.Message, UserFacingContextWindowMessage())
	}
	if payload.Error.Type != contextWindowExceededErrorType {
		t.Fatalf("type = %q, want %q", payload.Error.Type, contextWindowExceededErrorType)
	}
	if payload.Error.Code != contextWindowExceededErrorCode {
		t.Fatalf("code = %q, want %q", payload.Error.Code, contextWindowExceededErrorCode)
	}
}

func TestBuildErrorResponseBody_NormalizesContextWindowJSON(t *testing.T) {
	body := BuildErrorResponseBody(http.StatusBadRequest, `{"error":{"message":"invalid params, context window exceeds limit (2013)","type":"bad_request_error"}}`)

	var payload ErrorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Error.Message != UserFacingContextWindowMessage() {
		t.Fatalf("message = %q, want %q", payload.Error.Message, UserFacingContextWindowMessage())
	}
	if payload.Error.Type != contextWindowExceededErrorType {
		t.Fatalf("type = %q, want %q", payload.Error.Type, contextWindowExceededErrorType)
	}
	if payload.Error.Code != contextWindowExceededErrorCode {
		t.Fatalf("code = %q, want %q", payload.Error.Code, contextWindowExceededErrorCode)
	}
}

func TestIsContextWindowExceededError_DoesNotMatchBare2013(t *testing.T) {
	if IsContextWindowExceededError(http.StatusBadRequest, "provider error (2013)") {
		t.Fatal("bare 2013 should not match context-window classification")
	}
}
