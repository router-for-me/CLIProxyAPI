package logging

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
)

func TestSafeDiagnosticForLogPreservesAccessTokenExpiredAndRedactsCredentials(t *testing.T) {
	diagnostic := "access token expired\n" +
		`access_token=access-secret refresh token: refresh-secret Authorization=Bearer bearer-secret ` +
		`Post "https://user:password@oauth.example/token?access_token=query-secret" via socks5://proxy-user:proxy-password@127.0.0.1:1080`

	got := SafeDiagnosticForLog(diagnostic)
	if !strings.Contains(got, "access token expired") {
		t.Fatalf("safe diagnostic lost access-token-expired signal: %q", got)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "bearer-secret", "query-secret", "user:password", "proxy-user", "proxy-password"} {
		if strings.Contains(got, secret) {
			t.Fatalf("safe diagnostic leaked %q: %q", secret, got)
		}
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("safe diagnostic retained a line break: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("safe diagnostic did not mark redacted values: %q", got)
	}
}

func TestSafeDiagnosticForLogKeepsPlainAccessTokenExpiredMessage(t *testing.T) {
	const diagnostic = "access token expired"
	if got := SafeDiagnosticForLog(diagnostic); got != diagnostic {
		t.Fatalf("SafeDiagnosticForLog() = %q, want %q", got, diagnostic)
	}
}

func TestSafeDiagnosticForLogBoundsLargeMessageAndRetainsTrailingSignal(t *testing.T) {
	diagnostic := strings.Repeat("upstream context ", 1000) + "access token expired\nforged log line"
	got := SafeDiagnosticForLog(diagnostic)
	if len([]rune(got)) > diagnosticLogRuneLimit+3 {
		t.Fatalf("safe diagnostic length = %d, want at most %d", len([]rune(got)), diagnosticLogRuneLimit+3)
	}
	if !strings.Contains(got, "access token expired") {
		t.Fatalf("safe diagnostic lost trailing access-token-expired signal: %q", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("safe diagnostic retained a line break: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("safe diagnostic did not indicate truncation: %q", got)
	}
}

func TestSafeDiagnosticForLogBoundsLargeGenericMessage(t *testing.T) {
	got := SafeDiagnosticForLog(strings.Repeat("x", 900))
	if len([]rune(got)) != diagnosticLogRuneLimit+3 || !strings.HasSuffix(got, "...") {
		t.Fatalf("safe generic diagnostic length = %d, want %d with ellipsis", len([]rune(got)), diagnosticLogRuneLimit+3)
	}
}

func TestSafeErrorDiagnosticExtractsOnlyAllowlistedSignals(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantParts []string
	}{
		{name: "EOF", err: io.EOF, wantParts: []string{"EOF"}},
		{name: "SOCKS refused", err: errors.New("socks connect with unlabeled-secret: connection refused"), wantParts: []string{"proxy=socks", "connection_refused"}},
		{name: "OAuth response", err: errors.New(`upstream status 400 error="invalid_request" request_id="req-123" unlabeled-secret`), wantParts: []string{"status=400"}},
		{name: "unknown", err: errors.New("unlabeled-secret")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeErrorDiagnostic(tt.err)
			for _, want := range tt.wantParts {
				if !strings.Contains(got, want) {
					t.Fatalf("SafeErrorDiagnostic() = %q, want %q", got, want)
				}
			}
			if strings.Contains(got, "unlabeled-secret") {
				t.Fatalf("SafeErrorDiagnostic() leaked arbitrary detail: %q", got)
			}
		})
	}
}

func TestSafeErrorDiagnosticDoesNotExtractURLQueryValues(t *testing.T) {
	err := &url.Error{
		Op:  "Post",
		URL: "https://oauth.example/token?code=oauth-secret&error=error-secret&request_id=request-secret",
		Err: io.EOF,
	}

	got := SafeErrorDiagnostic(err)
	if !strings.Contains(got, "EOF") {
		t.Fatalf("SafeErrorDiagnostic() = %q, want EOF signal", got)
	}
	for _, secret := range []string{"oauth-secret", "error-secret", "request-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SafeErrorDiagnostic() leaked %q: %q", secret, got)
		}
	}
	for _, dynamicField := range []string{"oauth_error=", "request_id="} {
		if strings.Contains(got, dynamicField) {
			t.Fatalf("SafeErrorDiagnostic() extracted dynamic field %q: %q", dynamicField, got)
		}
	}
}
