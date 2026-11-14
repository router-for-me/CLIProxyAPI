package util

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplyCustomHeadersFromAttrs_StaticHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
	attrs := map[string]string{
		"header:X-Custom-Static": "static-value",
		"header:Host":            "custom.host.com",
	}

	ApplyCustomHeadersFromAttrs(req, attrs)

	if got := req.Header.Get("X-Custom-Static"); got != "static-value" {
		t.Errorf("X-Custom-Static = %q, want %q", got, "static-value")
	}
	if got := req.Host; got != "custom.host.com" {
		t.Errorf("req.Host = %q, want %q", got, "custom.host.com")
	}
}

func TestApplyCustomHeadersFromAttrs_MagicVariable(t *testing.T) {
	t.Run("present in clientHeaders sets header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Target-Session":         "$X-Claude-Code-Session-Id",
			"header:Static-Header":            "static-123",
		}
		clientHeaders := http.Header{
			"Abc":                      []string{"session-abc-456"},
			"X-Claude-Code-Session-Id": []string{"claude-code-uuid-789"},
		}

		ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)

		if got := req.Header.Get("X-Claude-Code-Session-Id"); got != "session-abc-456" {
			t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "session-abc-456")
		}
		if got := req.Header.Get("X-Target-Session"); got != "claude-code-uuid-789" {
			t.Errorf("X-Target-Session = %q, want %q", got, "claude-code-uuid-789")
		}
		if got := req.Header.Get("Static-Header"); got != "static-123" {
			t.Errorf("Static-Header = %q, want %q", got, "static-123")
		}
	})

	t.Run("absent in clientHeaders does not set header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Other":                  "$NONEXISTENT",
			"header:Static-Header":            "static-123",
		}
		clientHeaders := http.Header{
			"Other-Header": []string{"some-value"},
		}

		ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)

		if _, exists := req.Header["X-Claude-Code-Session-Id"]; exists {
			t.Errorf("expected X-Claude-Code-Session-Id to be omitted when $ABC is absent in clientHeaders, got %q", req.Header.Get("X-Claude-Code-Session-Id"))
		}
		if _, exists := req.Header["X-Other"]; exists {
			t.Errorf("expected X-Other to be omitted when $NONEXISTENT is absent in clientHeaders, got %q", req.Header.Get("X-Other"))
		}
		if got := req.Header.Get("Static-Header"); got != "static-123" {
			t.Errorf("Static-Header = %q, want %q", got, "static-123")
		}
	})

	t.Run("nil clientHeaders does not set variable headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:Static-Header":            "static-123",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if _, exists := req.Header["X-Claude-Code-Session-Id"]; exists {
			t.Errorf("expected X-Claude-Code-Session-Id to be omitted with nil clientHeaders, got %q", req.Header.Get("X-Claude-Code-Session-Id"))
		}
		if got := req.Header.Get("Static-Header"); got != "static-123" {
			t.Errorf("Static-Header = %q, want %q", got, "static-123")
		}
	})

	t.Run("fallback to gin context in request context", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(w)
		ginReq := httptest.NewRequest(http.MethodPost, "/", nil)
		ginReq.Header.Set("ABC", "from-gin-ctx-123")
		ginCtx.Request = ginReq

		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		req = req.WithContext(ginCtx)

		attrs := map[string]string{
			"header:X-Claude-Code-Session-Id": "$ABC",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if got := req.Header.Get("X-Claude-Code-Session-Id"); got != "from-gin-ctx-123" {
			t.Errorf("X-Claude-Code-Session-Id = %q, want %q", got, "from-gin-ctx-123")
		}
	})

	t.Run("cpa-session-id resolved from context", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		req = req.WithContext(WithSessionID(req.Context(), "claude:session-xyz-123"))
		attrs := map[string]string{
			"header:X-Session-ID":  "$CPA-SESSION-ID",
			"header:X-Auth-Token":  "Bearer $CPA-SESSION-ID",
			"header:X-Lower-Case":  "$cpa-session-id",
			"header:Static-Header": "static-val",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if got := req.Header.Get("X-Session-ID"); got != "claude:session-xyz-123" {
			t.Errorf("X-Session-ID = %q, want %q", got, "claude:session-xyz-123")
		}
		if got := req.Header.Get("X-Auth-Token"); got != "Bearer claude:session-xyz-123" {
			t.Errorf("X-Auth-Token = %q, want %q", got, "Bearer claude:session-xyz-123")
		}
		if got := req.Header.Get("X-Lower-Case"); got != "claude:session-xyz-123" {
			t.Errorf("X-Lower-Case = %q, want %q", got, "claude:session-xyz-123")
		}
		if got := req.Header.Get("Static-Header"); got != "static-val" {
			t.Errorf("Static-Header = %q, want %q", got, "static-val")
		}
	})

	t.Run("cpa-session-id cleared via WithSessionID empty string", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		ctx := WithSessionID(req.Context(), "initial-session-123")
		if got := SessionIDFromContext(ctx); got != "initial-session-123" {
			t.Fatalf("SessionIDFromContext before clear = %q, want %q", got, "initial-session-123")
		}

		clearedCtx := WithSessionID(ctx, "")
		if got := SessionIDFromContext(clearedCtx); got != "" {
			t.Fatalf("SessionIDFromContext after clear = %q, want empty", got)
		}

		req = req.WithContext(clearedCtx)
		attrs := map[string]string{
			"header:X-Session-ID": "$CPA-SESSION-ID",
			"header:X-Auth-Token": "Bearer $CPA-SESSION-ID",
			"header:Static":       "ok",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if _, exists := req.Header["X-Session-ID"]; exists {
			t.Errorf("expected X-Session-ID to be omitted when cleared, got %q", req.Header.Get("X-Session-ID"))
		}
		if _, exists := req.Header["X-Auth-Token"]; exists {
			t.Errorf("expected X-Auth-Token to be omitted when cleared, got %q", req.Header.Get("X-Auth-Token"))
		}
		if got := req.Header.Get("Static"); got != "ok" {
			t.Errorf("Static = %q, want %q", got, "ok")
		}
	})

	t.Run("cpa-session-id resolved from SessionIDResolver", func(t *testing.T) {
		oldResolver := SessionIDResolver
		defer func() { SessionIDResolver = oldResolver }()

		SessionIDResolver = func(ctx context.Context, clientHeaders http.Header) string {
			if clientHeaders != nil && clientHeaders.Get("X-Client-Sess") != "" {
				return "resolved:" + clientHeaders.Get("X-Client-Sess")
			}
			return ""
		}

		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Forwarded-Session": "$CPA-SESSION-ID",
		}
		clientHeaders := http.Header{
			"X-Client-Sess": []string{"sess-999"},
		}

		ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)

		if got := req.Header.Get("X-Forwarded-Session"); got != "resolved:sess-999" {
			t.Errorf("X-Forwarded-Session = %q, want %q", got, "resolved:sess-999")
		}
	})

	t.Run("cpa-session-id omitted when unresolvable", func(t *testing.T) {
		oldResolver := SessionIDResolver
		defer func() { SessionIDResolver = oldResolver }()
		SessionIDResolver = nil

		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Session-ID": "$CPA-SESSION-ID",
			"header:X-Auth-Token": "Bearer $CPA-SESSION-ID",
			"header:Static":       "ok",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if _, exists := req.Header["X-Session-ID"]; exists {
			t.Errorf("expected X-Session-ID to be omitted when unresolved, got %q", req.Header.Get("X-Session-ID"))
		}
		if _, exists := req.Header["X-Auth-Token"]; exists {
			t.Errorf("expected X-Auth-Token to be omitted when unresolved, got %q", req.Header.Get("X-Auth-Token"))
		}
		if got := req.Header.Get("Static"); got != "ok" {
			t.Errorf("Static = %q, want %q", got, "ok")
		}
	})

	t.Run("cpa-session-id does not read literal client header CPA-SESSION-ID", func(t *testing.T) {
		oldResolver := SessionIDResolver
		defer func() { SessionIDResolver = oldResolver }()
		SessionIDResolver = nil

		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		attrs := map[string]string{
			"header:X-Session-ID": "$CPA-SESSION-ID",
		}
		clientHeaders := http.Header{
			"Cpa-Session-Id": []string{"attacker-spoofed-value"},
		}

		ApplyCustomHeadersFromAttrs(req, attrs, clientHeaders)

		if _, exists := req.Header["X-Session-ID"]; exists {
			t.Errorf("expected X-Session-ID not to inherit spoofed client header CPA-SESSION-ID, got %q", req.Header.Get("X-Session-ID"))
		}
	})

	t.Run("cpa-session-id containing magic variable does not infinite loop", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com", nil)
		sessionWithToken := "header:$CPA-SESSION-ID"
		req = req.WithContext(WithSessionID(req.Context(), sessionWithToken))
		attrs := map[string]string{
			"header:X-Session-ID":  "$CPA-SESSION-ID",
			"header:X-Auth-Token":  "Bearer $CPA-SESSION-ID",
			"header:X-Auth-Repeat": "Bearer $cpa-session-id-$CPA-SESSION-ID",
		}

		ApplyCustomHeadersFromAttrs(req, attrs)

		if got := req.Header.Get("X-Session-ID"); got != sessionWithToken {
			t.Errorf("X-Session-ID = %q, want %q", got, sessionWithToken)
		}
		wantAuth := "Bearer " + sessionWithToken
		if got := req.Header.Get("X-Auth-Token"); got != wantAuth {
			t.Errorf("X-Auth-Token = %q, want %q", got, wantAuth)
		}
		wantRepeat := "Bearer " + sessionWithToken + "-" + sessionWithToken
		if got := req.Header.Get("X-Auth-Repeat"); got != wantRepeat {
			t.Errorf("X-Auth-Repeat = %q, want %q", got, wantRepeat)
		}
	})
}
