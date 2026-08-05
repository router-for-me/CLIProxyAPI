package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
)

func TestCodexTokenMetadataIncludesCredentialRevision(t *testing.T) {
	storage := &codex.CodexTokenStorage{
		IDToken:      "id-token",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "account-id",
		LastRefresh:  "2026-07-24T00:00:00Z",
		Email:        "user@example.com",
		Expire:       "2026-07-25T00:00:00Z",
	}

	metadata := codexTokenMetadata(storage)
	want := map[string]string{
		"type":          "codex",
		"id_token":      storage.IDToken,
		"access_token":  storage.AccessToken,
		"refresh_token": storage.RefreshToken,
		"account_id":    storage.AccountID,
		"last_refresh":  storage.LastRefresh,
		"email":         storage.Email,
		"expired":       storage.Expire,
	}
	for key, value := range want {
		if got, _ := metadata[key].(string); got != value {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, value)
		}
	}
}
