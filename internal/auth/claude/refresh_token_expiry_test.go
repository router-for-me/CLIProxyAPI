package claude

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseRefreshTokenExpiresIn(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   int64
		wantOK bool
	}{
		{name: "number", raw: `2592000`, want: 2592000, wantOK: true},
		{name: "numeric string", raw: `"2592000"`, want: 2592000, wantOK: true},
		{name: "padded numeric string", raw: `" 86400 "`, want: 86400, wantOK: true},
		{name: "fractional number", raw: `3600.9`, want: 3600, wantOK: true},
		{name: "absent", raw: ``, wantOK: false},
		{name: "null", raw: `null`, wantOK: false},
		{name: "empty string", raw: `""`, wantOK: false},
		{name: "non-numeric string", raw: `"soon"`, wantOK: false},
		{name: "zero", raw: `0`, wantOK: false},
		{name: "negative", raw: `-60`, wantOK: false},
		{name: "object", raw: `{"seconds":60}`, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRefreshTokenExpiresIn(json.RawMessage(tt.raw))
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("parseRefreshTokenExpiresIn(%q) = %d, %v; want %d, %v", tt.raw, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRefreshTokenExpiresAtFormatsUTC(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("UTC+8", 8*60*60))
	if got, want := refreshTokenExpiresAt(now, json.RawMessage(`"86400"`)), "2026-01-02T19:04:05Z"; got != want {
		t.Fatalf("refreshTokenExpiresAt() = %q, want %q", got, want)
	}
	if got := refreshTokenExpiresAt(now, nil); got != "" {
		t.Fatalf("refreshTokenExpiresAt(absent) = %q, want empty", got)
	}
}

// assertExpiresAround checks that value is an RFC3339 UTC timestamp roughly
// lifetime after the test started.
func assertExpiresAround(t *testing.T, value string, start time.Time, lifetime time.Duration) {
	t.Helper()
	expiresAt, errParse := time.Parse(time.RFC3339, value)
	if errParse != nil {
		t.Fatalf("refresh token expiry %q is not RFC3339: %v", value, errParse)
	}
	if expiresAt.Location() != time.UTC {
		t.Fatalf("refresh token expiry %q is not UTC", value)
	}
	lower := start.Add(lifetime).Add(-time.Second)
	upper := time.Now().Add(lifetime).Add(time.Second)
	if expiresAt.Before(lower) || expiresAt.After(upper) {
		t.Fatalf("refresh token expiry = %s, want between %s and %s", expiresAt, lower, upper)
	}
}

func newFakeTokenEndpointAuth(t *testing.T, tokenBody string) *ClaudeAuth {
	t.Helper()
	return &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case TokenURL:
					return jsonResponse(req, tokenBody), nil
				case ProfileURL:
					return jsonResponse(req, `{
						"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email":"user@example.com"},
						"organization":{"uuid":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Example Org"}
					}`), nil
				case RolesURL:
					return jsonResponse(req, `{"roles":[]}`), nil
				default:
					t.Fatalf("unexpected OAuth request URL %s", req.URL)
					return nil, nil
				}
			}),
		},
	}
}

func TestExchangeCodeForTokensRecordsRefreshTokenExpiry(t *testing.T) {
	start := time.Now()
	auth := newFakeTokenEndpointAuth(t, `{
		"access_token":"access",
		"refresh_token":"refresh",
		"expires_in":3600,
		"refresh_token_expires_in":2592000,
		"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"user@example.com"}
	}`)

	bundle, errExchange := auth.ExchangeCodeForTokens(t.Context(), "code", "state", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", errExchange)
	}
	assertExpiresAround(t, bundle.TokenData.RefreshTokenExpiresAt, start, 30*24*time.Hour)

	storage := auth.CreateTokenStorage(bundle)
	if storage.RefreshTokenExpiresAt != bundle.TokenData.RefreshTokenExpiresAt {
		t.Fatalf("storage refresh token expiry = %q, want %q", storage.RefreshTokenExpiresAt, bundle.TokenData.RefreshTokenExpiresAt)
	}

	path := filepath.Join(t.TempDir(), "claude.json")
	if errSave := storage.SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	saved := readSavedToken(t, path)
	if saved[RefreshTokenExpiresAtMetadataKey] != bundle.TokenData.RefreshTokenExpiresAt {
		t.Fatalf("saved %s = %v, want %q", RefreshTokenExpiresAtMetadataKey, saved[RefreshTokenExpiresAtMetadataKey], bundle.TokenData.RefreshTokenExpiresAt)
	}
}

func TestExchangeCodeForTokensWithoutRefreshTokenExpiry(t *testing.T) {
	auth := newFakeTokenEndpointAuth(t, `{
		"access_token":"access",
		"refresh_token":"refresh",
		"expires_in":3600,
		"account":{"uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","email_address":"user@example.com"}
	}`)

	bundle, errExchange := auth.ExchangeCodeForTokens(t.Context(), "code", "state", &PKCECodes{CodeVerifier: "verifier"})
	if errExchange != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", errExchange)
	}
	if bundle.TokenData.RefreshTokenExpiresAt != "" {
		t.Fatalf("refresh token expiry = %q, want empty", bundle.TokenData.RefreshTokenExpiresAt)
	}

	path := filepath.Join(t.TempDir(), "claude.json")
	if errSave := auth.CreateTokenStorage(bundle).SaveTokenToFile(path); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}
	if _, exists := readSavedToken(t, path)[RefreshTokenExpiresAtMetadataKey]; exists {
		t.Fatalf("saved credential contains %s, want it omitted", RefreshTokenExpiresAtMetadataKey)
	}
}

func TestRefreshTokensRecordsRefreshTokenExpiry(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	start := time.Now()
	auth := newFakeTokenEndpointAuth(t, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"refresh_token_expires_in":"86400"}`)
	tokenData, errRefresh := auth.RefreshTokens(t.Context(), "placeholder-refresh-with-expiry")
	if errRefresh != nil {
		t.Fatalf("RefreshTokens() error = %v", errRefresh)
	}
	assertExpiresAround(t, tokenData.RefreshTokenExpiresAt, start, 24*time.Hour)

	storage := &ClaudeTokenStorage{RefreshTokenExpiresAt: "2026-01-01T00:00:00Z"}
	auth.UpdateTokenStorage(storage, tokenData)
	if storage.RefreshTokenExpiresAt != tokenData.RefreshTokenExpiresAt {
		t.Fatalf("storage refresh token expiry = %q, want %q", storage.RefreshTokenExpiresAt, tokenData.RefreshTokenExpiresAt)
	}
}

func TestRefreshTokensWithoutRefreshTokenExpiryKeepsStoredValue(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	auth := newFakeTokenEndpointAuth(t, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	tokenData, errRefresh := auth.RefreshTokens(t.Context(), "placeholder-refresh-without-expiry")
	if errRefresh != nil {
		t.Fatalf("RefreshTokens() error = %v", errRefresh)
	}
	if tokenData.RefreshTokenExpiresAt != "" {
		t.Fatalf("refresh token expiry = %q, want empty", tokenData.RefreshTokenExpiresAt)
	}

	storage := &ClaudeTokenStorage{RefreshTokenExpiresAt: "2026-01-31T00:00:00Z"}
	auth.UpdateTokenStorage(storage, tokenData)
	if storage.RefreshTokenExpiresAt != "2026-01-31T00:00:00Z" {
		t.Fatalf("storage refresh token expiry = %q, want previous value kept", storage.RefreshTokenExpiresAt)
	}
}

func readSavedToken(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	return saved
}
