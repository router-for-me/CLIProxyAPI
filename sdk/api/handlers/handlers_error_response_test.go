package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type directResponseTestError struct {
	status int
	body   []byte
	direct bool
}

func (e directResponseTestError) Error() string        { return string(e.body) }
func (e directResponseTestError) StatusCode() int      { return e.status }
func (e directResponseTestError) DirectResponse() bool { return e.direct }
func (e directResponseTestError) ResponseBody() []byte { return e.body }

type responseBodyOnlyTestError struct {
	status int
	body   []byte
}

func (e responseBodyOnlyTestError) Error() string        { return string(e.body) }
func (e responseBodyOnlyTestError) StatusCode() int      { return e.status }
func (e responseBodyOnlyTestError) ResponseBody() []byte { return e.body }

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

func TestWriteErrorResponseDirectResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("X-Cpa-Trace-Id", "local-trace")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "https://trusted.example")

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode:     http.StatusForbidden,
		DirectResponse: true,
		Body:           []byte(`{"error":"blocked"}`),
		Headers: http.Header{
			"Content-Type":                {"application/problem+json"},
			"X-Plugin-Policy":             {"blocked"},
			"X-Cpa-Trace-Id":              {"plugin-trace"},
			"Access-Control-Allow-Origin": {"https://untrusted.example"},
		},
	})

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if got := recorder.Body.String(); got != `{"error":"blocked"}` {
		t.Fatalf("body = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-Plugin-Policy"); got != "blocked" {
		t.Fatalf("X-Plugin-Policy = %q", got)
	}
	if got := recorder.Header().Get("X-Cpa-Trace-Id"); got != "local-trace" {
		t.Fatalf("X-Cpa-Trace-Id = %q, want local value", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://trusted.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want trusted origin", got)
	}
}

func TestExecutionErrorMessagePreservesMarkedResponseBodyExactly(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        []byte
		contentType string
	}{
		{name: "json", status: http.StatusBadRequest, body: []byte(`{"error":"invalid_request"}`), contentType: "application/json"},
		{name: "text", status: http.StatusBadGateway, body: []byte("provider unavailable"), contentType: "text/plain; charset=utf-8"},
		{name: "multiline", status: http.StatusTooManyRequests, body: []byte("first line\r\nsecond line\n"), contentType: "text/plain; charset=utf-8"},
		{name: "empty", status: http.StatusUnauthorized, body: []byte{}, contentType: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

			errUpstream := directResponseTestError{status: tt.status, body: tt.body, direct: true}
			msg := executionErrorMessage(errUpstream)
			if msg == nil || !msg.DirectResponse {
				t.Fatalf("executionErrorMessage() direct response = %#v, want true", msg)
			}
			NewBaseAPIHandlers(nil, nil).WriteErrorResponse(c, msg)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.status)
			}
			if got := recorder.Body.Bytes(); !bytes.Equal(got, tt.body) {
				t.Fatalf("body = %q, want exact body %q", got, tt.body)
			}
			if got := recorder.Header().Get("Content-Type"); got != tt.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.contentType)
			}
		})
	}
}

func TestExecutionErrorMessageDoesNotTrustResponseBodyWithoutMarker(t *testing.T) {
	errUnmarked := responseBodyOnlyTestError{
		status: http.StatusBadGateway,
		body:   []byte("unmarked upstream body"),
	}
	msg := executionErrorMessage(errUnmarked)
	if msg == nil {
		t.Fatal("executionErrorMessage() returned nil")
	}
	if msg.DirectResponse {
		t.Fatal("executionErrorMessage() trusted an unmarked response body")
	}
}

func TestInternalConcurrencyBusyWritesRetryAfterWithoutPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewHomeConcurrencyBusyError("busy", 750*time.Millisecond),
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

func TestWriteErrorResponseHomeBusyNormalAndStreamHeaders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stream"}[stream], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if stream {
				c.Request.Header.Set("Accept", "text/event-stream")
			}

			handler := NewBaseAPIHandlers(nil, nil)
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      coreauth.NewHomeConcurrencyBusyError("busy", 750*time.Millisecond),
			})
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
			}
			if got := recorder.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		})
	}
}

func TestWriteErrorResponse_AddonHeadersEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Writer.Header().Set("X-Request-Id", "old-value")
	c.Writer.Header().Set("x-cpa-trace-id", "local-trace")
	c.Writer.Header().Set("Access-Control-Expose-Headers", "x-cpa-trace-id")

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":                   {"30"},
			"X-Request-Id":                  {"new-1", "new-2"},
			"x-cpa-trace-id":                {"upstream-trace"},
			"Access-Control-Expose-Headers": {"upstream-header"},
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
	if got := recorder.Header().Get("x-cpa-trace-id"); got != "local-trace" {
		t.Fatalf("x-cpa-trace-id = %q, want local trace", got)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != "x-cpa-trace-id" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want CPA value", got)
	}
}

func TestEnrichAuthSelectionError_DefaultsTo503WithContext(t *testing.T) {
	in := &coreauth.Error{Code: "auth_not_found", Message: "no auth available"}
	out := enrichAuthSelectionError(in, []string{"claude"}, "claude-sonnet-4-6")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusServiceUnavailable)
	}
	if !strings.Contains(got.Message, "providers=claude") {
		t.Fatalf("message missing provider context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "model=claude-sonnet-4-6") {
		t.Fatalf("message missing model context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "/v0/management/auth-files") {
		t.Fatalf("message missing management hint: %q", got.Message)
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

func TestExecutionErrorMessageMapsContextStatuses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "canceled", err: context.Canceled, want: clienterror.StatusClientClosedRequest},
		{name: "deadline", err: context.DeadlineExceeded, want: http.StatusGatewayTimeout},
		{
			name: "url error wraps canceled",
			err:  &url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled},
			want: clienterror.StatusClientClosedRequest,
		},
		{name: "plain error defaults to 500", err: errors.New("boom"), want: http.StatusInternalServerError},
		{
			name: "explicit status wins",
			err:  &coreauth.Error{Code: "rate_limited", Message: "slow down", HTTPStatus: http.StatusTooManyRequests},
			want: http.StatusTooManyRequests,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := executionErrorMessage(tc.err)
			if msg == nil {
				t.Fatalf("executionErrorMessage() returned nil")
			}
			if msg.StatusCode != tc.want {
				t.Fatalf("StatusCode = %d, want %d", msg.StatusCode, tc.want)
			}
			if msg.Error != tc.err {
				t.Fatalf("Error = %v, want original %v", msg.Error, tc.err)
			}
		})
	}
}

func TestStatusFromErrorMapsContextStatuses(t *testing.T) {
	if got := statusFromError(context.Canceled); got != clienterror.StatusClientClosedRequest {
		t.Fatalf("statusFromError(canceled) = %d, want %d", got, clienterror.StatusClientClosedRequest)
	}
	if got := statusFromError(context.DeadlineExceeded); got != http.StatusGatewayTimeout {
		t.Fatalf("statusFromError(deadline) = %d, want %d", got, http.StatusGatewayTimeout)
	}
	if got := statusFromError(&url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled}); got != clienterror.StatusClientClosedRequest {
		t.Fatalf("statusFromError(url canceled) = %d, want %d", got, clienterror.StatusClientClosedRequest)
	}
	if got := statusFromError(errors.New("boom")); got != 0 {
		t.Fatalf("statusFromError(plain) = %d, want 0", got)
	}
}

func TestWriteErrorResponse_ContextCanceledUses499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, executionErrorMessage(context.Canceled))

	if recorder.Code != clienterror.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, clienterror.StatusClientClosedRequest)
	}
}
