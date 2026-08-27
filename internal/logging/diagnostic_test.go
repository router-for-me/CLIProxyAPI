package logging

import (
	"strings"
	"testing"
)

func TestSafeDiagnosticForLogPreservesAccessTokenExpiredAndRedactsCredentials(t *testing.T) {
	diagnostic := "access token expired\n" +
		`access_token=access-secret refresh token: refresh-secret Authorization=Bearer bearer-secret ` +
		`Post "https://user:password@oauth.example/token?access_token=query-secret"`

	got := SafeDiagnosticForLog(diagnostic)
	if !strings.Contains(got, "access token expired") {
		t.Fatalf("safe diagnostic lost access-token-expired signal: %q", got)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "bearer-secret", "query-secret", "user:password"} {
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
