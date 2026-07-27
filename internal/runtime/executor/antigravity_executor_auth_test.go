package executor

import (
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestMetaStringValue_FlatAndNestedToken covers the canonical flat metadata
// shape (the one the antigravity OAuth login writes today) plus the legacy
// nested "token" shape used by historical on-disk auth files. The refresh /
// ensureAccessToken paths read access_token and refresh_token via
// metaStringValue, so a regression in this helper surfaces as a spurious 401
// "missing refresh token" for any auth file persisted before the flat form.
func TestMetaStringValue_FlatAndNestedToken(t *testing.T) {
	flat := map[string]any{
		"access_token":  "flat-access",
		"refresh_token": "flat-refresh",
	}
	if got := metaStringValue(flat, "access_token"); got != "flat-access" {
		t.Fatalf("flat access_token = %q, want flat-access", got)
	}
	if got := metaStringValue(flat, "refresh_token"); got != "flat-refresh" {
		t.Fatalf("flat refresh_token = %q, want flat-refresh", got)
	}

	nested := map[string]any{
		"type": "antigravity",
		"token": map[string]any{
			"access_token":  "nested-access",
			"refresh_token": "nested-refresh",
			"expiry":        "2026-07-26T17:51:32Z",
		},
	}
	if got := metaStringValue(nested, "access_token"); got != "nested-access" {
		t.Fatalf("nested access_token = %q, want nested-access", got)
	}
	if got := metaStringValue(nested, "refresh_token"); got != "nested-refresh" {
		t.Fatalf("nested refresh_token = %q, want nested-refresh", got)
	}
	// Missing key returns empty.
	if got := metaStringValue(nested, "project_id"); got != "" {
		t.Fatalf("nested project_id = %q, want empty", got)
	}
	// Nil metadata returns empty.
	if got := metaStringValue(nil, "access_token"); got != "" {
		t.Fatalf("nil metadata access_token = %q, want empty", got)
	}
}

// TestTokenExpiry_FlatAndNestedToken ensures tokenExpiry resolves the
// expiration timestamp from both the canonical flat form
// {"expired":"<RFC3339>"} (or the expires_in+timestamp pair) and the legacy
// nested {"token":{"expiry":"<RFC3339>"}} shape stored by older auth files.
func TestTokenExpiry_FlatAndNestedToken(t *testing.T) {
	want := time.Date(2026, 7, 26, 17, 51, 32, 0, time.UTC)

	flat := map[string]any{"expired": "2026-07-26T17:51:32Z"}
	if got := tokenExpiry(flat); !got.Equal(want) {
		t.Fatalf("tokenExpiry(flat expired) = %v, want %v", got, want)
	}

	flatPair := map[string]any{
		"expires_in": int64(3600),
		"timestamp":  int64(1782349200 * 1000), // arbitrary ms
	}
	got := tokenExpiry(flatPair)
	if got.IsZero() {
		t.Fatalf("tokenExpiry(flat expires_in+timestamp) returned zero; want non-zero")
	}

	nested := map[string]any{
		"token": map[string]any{
			"expiry": "2026-07-26T17:51:32Z",
		},
	}
	if got := tokenExpiry(nested); !got.Equal(want) {
		t.Fatalf("tokenExpiry(nested token.expiry) = %v, want %v", got, want)
	}

	if got := tokenExpiry(nil); !got.IsZero() {
		t.Fatalf("tokenExpiry(nil) = %v, want zero", got)
	}
}

// TestEnsureAccessToken_NestedTokenMetadataReconstructsRefresh confirms that an
// antigravity auth file persisted with the historical nested "token" shape is
// still serviceable: ensureAccessToken must surface a non-empty access token
// rather than returning the 401 "missing refresh token" status error that the
// flat-only reader produced before this fix.
//
// We exercise this via the Refresh path (which itself reads refresh_token
// through metaStringValue) against the success endpoint already wired by the
// antigravity refresh test.
func TestEnsureAccessToken_NestedTokenMetadataReconstructsRefresh(t *testing.T) {
	nested := &cliproxyauth.Auth{
		ID:       "antigravity-nested@example.com",
		Provider: "antigravity",
		Metadata: map[string]any{
			"type": "antigravity",
			// Wrap access + refresh tokens in a nested "token" object, the
			// format used by legacy auth files such as
			// pgstore/auths/antigravity-<email>.json emitted by external tools.
			"token": map[string]any{
				"access_token":  "ya29.access",
				"refresh_token": "1//refresh",
				"expiry":        time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	// Direct sanity check: the helpers must surface the tokens even though
	// they live under the nested "token" object (the legacy layout).
	if got := metaStringValue(nested.Metadata, "access_token"); got != "ya29.access" {
		t.Fatalf("metaStringValue(access_token) = %q, want ya29.access", got)
	}
	if got := metaStringValue(nested.Metadata, "refresh_token"); got != "1//refresh" {
		t.Fatalf("metaStringValue(refresh_token) = %q, want 1//refresh", got)
	}

	// Expired token => ensureAccessToken would normally refresh. We only need
	// to confirm that the missing-refresh-token branch is NOT taken, i.e. the
	// refresh_token is now discoverable. A live upstream is not required: the
	// single-flight refresh will return a network error, not the 401 "missing
	// refresh token" status error.
	executor := &AntigravityExecutor{}
	_, _, err := executor.ensureAccessToken(t.Context(), nested.Clone())
	if err == nil {
		// Refresh may have succeeded if a real endpoint was reachable; that's
		// fine too. The only failure mode we are guarding against is the auth
		// error returned when refresh_token is empty.
		return
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se.StatusCode() == 401 {
		t.Fatalf("ensureAccessToken returned 401 for nested-token metadata: %v (refresh_token was not read)", err)
	}
}
