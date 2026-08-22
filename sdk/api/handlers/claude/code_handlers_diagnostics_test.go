package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type claudeBootstrapHeaderError struct {
	message string
	headers http.Header
}

func (e *claudeBootstrapHeaderError) Error() string        { return e.message }
func (e *claudeBootstrapHeaderError) Headers() http.Header { return e.headers.Clone() }

func TestClaudeStreamBootstrapDiagnosticClassifiesLocalAuth503(t *testing.T) {
	fields, ok := claudeStreamBootstrapDiagnosticFields(nil, context.Background(), "initial_error", "gpt-5.4", &interfaces.ErrorMessage{
		StatusCode: http.StatusServiceUnavailable,
		Error: &coreauth.Error{
			Code:       "auth_unavailable",
			Message:    "no auth available",
			HTTPStatus: http.StatusServiceUnavailable,
		},
	})
	if !ok {
		t.Fatal("expected diagnostic fields for HTTP 503")
	}
	if got := fields["phase"]; got != "initial_error" {
		t.Fatalf("phase = %#v, want initial_error", got)
	}
	if got := fields["auth_selected"]; got != false {
		t.Fatalf("auth_selected = %#v, want false", got)
	}
	if got := fields["upstream_response_seen"]; got != false {
		t.Fatalf("upstream_response_seen = %#v, want false", got)
	}
	if got := fields["auth_code"]; got != "auth_unavailable" {
		t.Fatalf("auth_code = %#v, want auth_unavailable", got)
	}
}

func TestClaudeStreamBootstrapDiagnosticClassifiesSanitizedUpstream503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	internallogging.SetGinRequestID(c, "request\nid")
	internallogging.SetGinCPATraceID(c, "credential-secret-name")

	longRequestID := "req\n" + strings.Repeat("x", 600)
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Retry-After":           []string{"2\r\nseconds"},
		"X-Upstream-Request-Id": []string{longRequestID},
		"Authorization":         []string{"Bearer do-not-log"},
		"Set-Cookie":            []string{"session=do-not-log"},
	})
	fields, ok := claudeStreamBootstrapDiagnosticFields(c, ctx, "closed_before_first_chunk", "gpt-5.4\nforged", &interfaces.ErrorMessage{
		StatusCode: http.StatusServiceUnavailable,
		Error: &claudeBootstrapHeaderError{
			message: "token=do-not-log",
			headers: http.Header{"X-Internal-Secret": []string{"do-not-log"}},
		},
	})
	if !ok {
		t.Fatal("expected diagnostic fields for HTTP 503")
	}
	if got := fields["phase"]; got != "closed_before_first_chunk" {
		t.Fatalf("phase = %#v, want closed_before_first_chunk", got)
	}
	if got := fields["auth_selected"]; got != true {
		t.Fatalf("auth_selected = %#v, want true", got)
	}
	if got := fields["upstream_response_seen"]; got != true {
		t.Fatalf("upstream_response_seen = %#v, want true", got)
	}
	if got := fields["retry_after"]; got != "2 seconds" {
		t.Fatalf("retry_after = %#v, want sanitized value", got)
	}
	upstreamRequestID, _ := fields["upstream_request_id"].(string)
	if strings.ContainsAny(upstreamRequestID, "\r\n") || len([]rune(upstreamRequestID)) > claudeStreamBootstrapDiagnosticValueLimit+1 {
		t.Fatalf("upstream_request_id was not bounded: %q", upstreamRequestID)
	}
	formatted := fmt.Sprint(fields)
	for _, secret := range []string{"credential-secret-name", "Bearer do-not-log", "session=do-not-log", "token=do-not-log", "X-Internal-Secret"} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("diagnostic fields leaked %q: %s", secret, formatted)
		}
	}
}

func TestClaudeStreamBootstrapDiagnosticRecognizesHeaderBearingUpstreamError(t *testing.T) {
	fields, ok := claudeStreamBootstrapDiagnosticFields(nil, context.Background(), "initial_error", "gpt-5.4", &interfaces.ErrorMessage{
		StatusCode: http.StatusServiceUnavailable,
		Error:      &claudeBootstrapHeaderError{message: "unavailable"},
	})
	if !ok || fields["upstream_response_seen"] != true {
		t.Fatalf("fields = %#v, ok = %v; want upstream response", fields, ok)
	}
}

func TestClaudeStreamBootstrapDiagnosticIgnoresNon503(t *testing.T) {
	fields, ok := claudeStreamBootstrapDiagnosticFields(nil, context.Background(), "initial_error", "gpt-5.4", &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
	})
	if ok || fields != nil {
		t.Fatalf("fields = %#v, ok = %v; want no diagnostic", fields, ok)
	}
}
