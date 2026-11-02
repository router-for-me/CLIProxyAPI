package kiro

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRefreshTokenFingerprint(t *testing.T) {
	if got := RefreshTokenFingerprint(" token "); got == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if got := RefreshTokenFingerprint("token"); got != RefreshTokenFingerprint("token") {
		t.Fatal("expected stable fingerprint")
	}
}

func TestDuplicateReason(t *testing.T) {
	existing := &coreauth.Auth{
		ID:       "kiro-1",
		Provider: "kiro",
		Metadata: map[string]any{
			metadataRefreshTokenFingerprint: RefreshTokenFingerprint("refresh-a"),
			metadataOriginalRefreshTokenFP:  RefreshTokenFingerprint("refresh-a"),
			metadataKiroAPIKey:              "api-key-a",
		},
	}

	candidate := &coreauth.Auth{
		ID:       "kiro-2",
		Provider: "kiro",
		Metadata: map[string]any{
			metadataRefreshTokenFingerprint: RefreshTokenFingerprint("refresh-a"),
		},
	}
	if reason := DuplicateReason(existing, candidate); reason != "duplicate refresh token" {
		t.Fatalf("unexpected duplicate reason: %q", reason)
	}

	candidate = &coreauth.Auth{
		ID:       "kiro-3",
		Provider: "kiro",
		Metadata: map[string]any{
			metadataRefreshTokenFingerprint: RefreshTokenFingerprint("refresh-b"),
			metadataOriginalRefreshTokenFP:  RefreshTokenFingerprint("refresh-a"),
		},
	}
	if reason := DuplicateReason(existing, candidate); reason != "duplicate original refresh token" {
		t.Fatalf("unexpected duplicate reason: %q", reason)
	}

	candidate = &coreauth.Auth{
		ID:       "kiro-4",
		Provider: "kiro",
		Metadata: map[string]any{
			metadataKiroAPIKey: "api-key-a",
		},
	}
	if reason := DuplicateReason(existing, candidate); reason != "duplicate API key" {
		t.Fatalf("unexpected duplicate reason: %q", reason)
	}
}

func TestEnsureAuthMetadataFingerprint(t *testing.T) {
	auth := &coreauth.Auth{
		Metadata: map[string]any{
			metadataRefreshToken: "refresh-token",
		},
	}
	EnsureAuthMetadataFingerprint(auth)
	if got := MetadataString(auth.Metadata, metadataRefreshTokenFingerprint); got == "" {
		t.Fatal("expected refresh fingerprint")
	}
	if got := MetadataString(auth.Metadata, metadataOriginalRefreshTokenFP); got == "" {
		t.Fatal("expected original refresh fingerprint")
	}
}

func TestNeedsTokenRefresh(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if NeedsTokenRefresh(nil, now) {
		t.Fatal("nil credentials should not need refresh")
	}
	if NeedsTokenRefresh(&KiroCredentials{AuthMethod: "api_key", RefreshToken: "rt"}, now) {
		t.Fatal("api_key credentials should not need refresh")
	}
	if !NeedsTokenRefresh(&KiroCredentials{AuthMethod: "idc", RefreshToken: "rt", AccessToken: ""}, now) {
		t.Fatal("missing access token with refresh token should need refresh")
	}
	if !NeedsTokenRefresh(&KiroCredentials{AuthMethod: "idc", RefreshToken: "rt", AccessToken: "at", ExpiresAt: now.Unix() + 60}, now) {
		t.Fatal("token expiring within 120s should need refresh")
	}
	if NeedsTokenRefresh(&KiroCredentials{AuthMethod: "idc", RefreshToken: "rt", AccessToken: "at", ExpiresAt: now.Unix() + 300}, now) {
		t.Fatal("fresh token should not need refresh")
	}
}

func TestIsInvalidBearerTokenError(t *testing.T) {
	if !IsInvalidBearerTokenError(http.StatusUnauthorized, []byte(`{}`)) {
		t.Fatal("expected 401 to be treated as invalid bearer token")
	}
	if !IsInvalidBearerTokenError(http.StatusForbidden, []byte(`{"message":"The bearer token included in the request is invalid.","reason":null}`)) {
		t.Fatal("expected Kiro 403 invalid bearer message to be detected")
	}
	if IsInvalidBearerTokenError(http.StatusForbidden, []byte(`{"message":"AccessDeniedException: subscription required"}`)) {
		t.Fatal("non-token 403 should not be treated as invalid bearer token")
	}
}

