package auth

import (
	"encoding/json"
	"testing"
)

// TestMetadataEqualIgnoringTimestamps_AccessTokenNotIgnoredForGoogleProviders
// Verifies that access_token changes are detected for Google OAuth providers
// (gemini, gemini-cli, antigravity), ensuring tokens are persisted after refresh.
//
// This test addresses GitHub Issue #833:
// "Antigravity OAuth tokens expire despite having refresh_token stored"
// The bug was caused by metadataEqualIgnoringTimestamps() ignoring access_token
// for Google providers, which prevented token persistence after refresh.
func TestMetadataEqualIgnoringTimestamps_AccessTokenNotIgnoredForGoogleProviders(t *testing.T) {
	t.Parallel()

	// Simulate a token refresh scenario: only access_token and timestamp change
	oldToken := `{"access_token":"old_token","refresh_token":"refresh123","expires_in":3600,"expired":"2024-01-01T00:00:00Z","timestamp":"2024-01-01T00:00:00Z"}`
	newToken := `{"access_token":"new_token","refresh_token":"refresh123","expires_in":3600,"expired":"2024-01-01T01:00:00Z","timestamp":"2024-01-01T01:00:00Z"}`

	testCases := []struct {
		name     string
		provider string
		want     bool // true means equal (no write needed), false means different (write needed)
	}{
		{
			name:     "gemini provider detects access_token change",
			provider: "gemini",
			want:     false, // access_token should NOT be ignored, so tokens are different
		},
		{
			name:     "gemini-cli provider detects access_token change",
			provider: "gemini-cli",
			want:     false, // access_token should NOT be ignored
		},
		{
			name:     "antigravity provider detects access_token change",
			provider: "antigravity",
			want:     false, // access_token should NOT be ignored
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadataEqualIgnoringTimestamps([]byte(oldToken), []byte(newToken), tc.provider)
			if got != tc.want {
				t.Errorf("metadataEqualIgnoringTimestamps() = %v, want %v for provider %s", got, tc.want, tc.provider)
			}
		})
	}
}

// TestMetadataEqualIgnoringTimestamps_TimestampFieldsIgnored
// Verifies that timestamp-related fields (timestamp, expired, expires_in, last_refresh)
// are correctly ignored regardless of provider, preventing unnecessary file writes.
func TestMetadataEqualIgnoringTimestamps_TimestampFieldsIgnored(t *testing.T) {
	t.Parallel()

	// Only timestamp-related fields change
	oldMetadata := `{"access_token":"token123","email":"test@example.com","timestamp":"2024-01-01T00:00:00Z","expired":"2024-01-01T00:00:00Z","expires_in":3600}`
	newMetadata := `{"access_token":"token123","email":"test@example.com","timestamp":"2024-01-01T01:00:00Z","expired":"2024-01-01T01:00:00Z","expires_in":3600}`

	// Should be equal because only timestamp fields changed
	got := metadataEqualIgnoringTimestamps([]byte(oldMetadata), []byte(newMetadata), "gemini")
	if !got {
		t.Errorf("metadataEqualIgnoringTimestamps() should return true when only timestamp fields change, got %v", got)
	}
}

// TestMetadataEqualIgnoringTimestamps_RefreshTokenPreserved
// Verifies that refresh_token is compared (not ignored) and changes are detected.
func TestMetadataEqualIgnoringTimestamps_RefreshTokenPreserved(t *testing.T) {
	t.Parallel()

	oldMetadata := `{"access_token":"token123","refresh_token":"old_refresh","expires_in":3600}`
	newMetadata := `{"access_token":"token123","refresh_token":"new_refresh","expires_in":3600}`

	// refresh_token should NOT be ignored, so tokens should be different
	got := metadataEqualIgnoringTimestamps([]byte(oldMetadata), []byte(newMetadata), "antigravity")
	if got {
		t.Errorf("metadataEqualIgnoringTimestamps() should detect refresh_token change for antigravity, got %v", got)
	}
}

// TestMetadataEqualIgnoringTimestamps_NonGoogleProvider
// Verifies behavior for non-Google providers (like iFlow) to ensure we don't break other providers.
func TestMetadataEqualIgnoringTimestamps_NonGoogleProvider(t *testing.T) {
	t.Parallel()

	oldMetadata := `{"access_token":"token123","api_key":"key456","timestamp":"2024-01-01T00:00:00Z"}`
	newMetadata := `{"access_token":"new_token","api_key":"key456","timestamp":"2024-01-01T01:00:00Z"}`

	// For non-Google providers, access_token is also not ignored
	got := metadataEqualIgnoringTimestamps([]byte(oldMetadata), []byte(newMetadata), "iflow")
	if got {
		t.Errorf("metadataEqualIgnoringTimestamps() should detect access_token change for non-Google provider, got %v", got)
	}
}

// TestMetadataEqualIgnoringTimestamps_EmptyTokens
// Verifies edge case handling for empty or invalid JSON.
func TestMetadataEqualIgnoringTimestamps_EmptyTokens(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		a        string
		b        string
		provider string
		want     bool
	}{
		{
			name:     "both empty",
			a:        `{}`,
			b:        `{}`,
			provider: "gemini",
			want:     true,
		},
		{
			name:     "one empty",
			a:        `{}`,
			b:        `{"access_token":"test"}`,
			provider: "gemini",
			want:     false,
		},
		{
			name:     "identical tokens",
			a:        `{"access_token":"token123","refresh_token":"refresh"}`,
			b:        `{"access_token":"token123","refresh_token":"refresh"}`,
			provider: "antigravity",
			want:     true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadataEqualIgnoringTimestamps([]byte(tc.a), []byte(tc.b), tc.provider)
			if got != tc.want {
				t.Errorf("metadataEqualIgnoringTimestamps() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMetadataEqualIgnoringTimestamps_TokenRefreshScenario
// Simulates the actual token refresh scenario from the bug report:
// Token is refreshed in memory,_token changes while but access other fields stay same.
// The file should be written because access_token is now different.
func TestMetadataEqualIgnoringTimestamps_TokenRefreshScenario(t *testing.T) {
	t.Parallel()

	// This is the exact scenario from GitHub Issue #833:
	// 1. User logs in, access_token is stored
	// 2. Token expires after ~1 hour
	// 3. antigravity_executor.refreshToken() is called
	// 4. Token is refreshed in memory with new access_token
	// 5. File should be written with new access_token

	// Stored token (expired)
	storedToken := `{
		"access_token": "dummy_access_token_old",
		"refresh_token": "dummy_refresh_token",
		"expires_in": 3599,
		"expired": "2024-01-26T01:28:42+08:00",
		"timestamp": "2024-01-26T01:28:42+08:00"
	}`

	// Refreshed token (new access_token, new expiry)
	refreshedToken := `{
		"access_token": "dummy_access_token_new",
		"refresh_token": "dummy_refresh_token",
		"expires_in": 3599,
		"expired": "2024-01-26T02:28:42+08:00",
		"timestamp": "2024-01-26T02:28:42+08:00"
	}`

	// For antigravity provider, access_token change should be detected
	// This means the file WILL be written after refresh (the fix!)
	got := metadataEqualIgnoringTimestamps([]byte(storedToken), []byte(refreshedToken), "antigravity")
	if got {
		t.Errorf("Token refresh scenario: metadataEqualIgnoringTimestamps() should detect access_token change after refresh, got %v (BUG: token will not be saved!)", got)
	}
}

// TestMetadataEqualIgnoringTimestamps_LastRefreshField
// Verifies that last_refresh field is also ignored (was added to ignoredFields list).
func TestMetadataEqualIgnoringTimestamps_LastRefreshField(t *testing.T) {
	t.Parallel()

	oldMetadata := `{"access_token":"token123","last_refresh":"2024-01-01T00:00:00Z"}`
	newMetadata := `{"access_token":"token123","last_refresh":"2024-01-01T01:00:00Z"}`

	// last_refresh should be ignored
	got := metadataEqualIgnoringTimestamps([]byte(oldMetadata), []byte(newMetadata), "gemini")
	if !got {
		t.Errorf("metadataEqualIgnoringTimestamps() should ignore last_refresh field, got %v", got)
	}
}

// TestMetadataEqualIgnoringTimestamps_ComplexNestedFields
// Verifies that non-timestamp nested fields are properly compared.
func TestMetadataEqualIgnoringTimestamps_ComplexNestedFields(t *testing.T) {
	t.Parallel()

	oldMetadata := `{"access_token":"token123","config":{"model":"gemini-pro","temperature":0.7},"expires_in":3600}`
	newMetadata := `{"access_token":"token123","config":{"model":"gemini-pro","temperature":0.9},"expires_in":3600}`

	// Only expires_in changes, access_token stays same, config changes
	// Should be different because config changed (not a timestamp field)
	got := metadataEqualIgnoringTimestamps([]byte(oldMetadata), []byte(newMetadata), "gemini")
	if got {
		t.Errorf("metadataEqualIgnoringTimestamps() should detect config changes, got %v", got)
	}
}

// TestMetadataEqualIgnoringTimestamps_RoundTrip
// Tests that the function works correctly with JSON unmarshaling round-trip.
func TestMetadataEqualIgnoringTimestamps_RoundTrip(t *testing.T) {
	t.Parallel()

	original := map[string]any{
		"access_token": "token123",
		"refresh_token": "refresh456",
		"expires_in":   3599,
		"expired":      "2024-01-01T01:00:00Z",
		"timestamp":    "2024-01-01T01:00:00Z",
	}

	// Modify only timestamp fields
	modified := map[string]any{
		"access_token": "token123",
		"refresh_token": "refresh456",
		"expires_in":   3599,
		"expired":      "2024-01-01T02:00:00Z",
		"timestamp":    "2024-01-01T02:00:00Z",
	}

	originalJSON, _ := json.Marshal(original)
	modifiedJSON, _ := json.Marshal(modified)

	got := metadataEqualIgnoringTimestamps(originalJSON, modifiedJSON, "antigravity")
	if !got {
		t.Errorf("metadataEqualIgnoringTimestamps() should return true for round-trip with only timestamp changes, got %v", got)
	}
}
