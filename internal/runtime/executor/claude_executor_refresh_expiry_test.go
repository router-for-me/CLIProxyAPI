package executor

import (
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestApplyClaudeRefreshedTokensStoresRefreshTokenExpiry(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"refresh_token": "old-refresh",
		claudeauth.RefreshTokenExpiresAtMetadataKey: "2026-01-01T00:00:00Z",
	}}

	applyClaudeRefreshedTokens(auth, &claudeauth.ClaudeTokenData{
		AccessToken:           "new-access",
		RefreshToken:          "new-refresh",
		Expire:                "2026-01-02T00:00:00Z",
		RefreshTokenExpiresAt: "2026-01-31T00:00:00Z",
	})

	if got := auth.Metadata[claudeauth.RefreshTokenExpiresAtMetadataKey]; got != "2026-01-31T00:00:00Z" {
		t.Fatalf("refresh_token_expires_at = %#v, want refreshed value", got)
	}
	if got := auth.Metadata["refresh_token"]; got != "new-refresh" {
		t.Fatalf("refresh_token = %#v, want new-refresh", got)
	}
}

func TestApplyClaudeRefreshedTokensKeepsRefreshTokenExpiryWhenOmitted(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		claudeauth.RefreshTokenExpiresAtMetadataKey: "2026-01-31T00:00:00Z",
	}}

	applyClaudeRefreshedTokens(auth, &claudeauth.ClaudeTokenData{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		Expire:       "2026-01-02T00:00:00Z",
	})

	if got := auth.Metadata[claudeauth.RefreshTokenExpiresAtMetadataKey]; got != "2026-01-31T00:00:00Z" {
		t.Fatalf("refresh_token_expires_at = %#v, want previous value kept", got)
	}
}

func TestApplyClaudeRefreshedTokensLeavesRefreshTokenExpiryAbsent(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{}}

	applyClaudeRefreshedTokens(auth, &claudeauth.ClaudeTokenData{AccessToken: "new-access", RefreshToken: "new-refresh"})

	if got, exists := auth.Metadata[claudeauth.RefreshTokenExpiresAtMetadataKey]; exists {
		t.Fatalf("refresh_token_expires_at = %#v, want absent", got)
	}
}
