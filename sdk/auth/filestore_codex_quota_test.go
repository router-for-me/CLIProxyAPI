package auth

import (
	"context"
	"testing"

	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStoreSave_CodexQuotaMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewFileTokenStore()
	baseDir := t.TempDir()
	store.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		ID:       "codex-user.json",
		Provider: "codex",
		FileName: "codex-user.json",
		Storage: &internalcodex.CodexTokenStorage{
			IDToken:      "id-token",
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			AccountID:    "acct-123",
			LastRefresh:  "2026-03-26T00:00:00Z",
			Email:        "user@example.com",
			Expire:       "2026-03-27T00:00:00Z",
		},
		Metadata: map[string]any{
			"type":       "codex",
			"account_id": "acct-123",
			"codex_quota": map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"used_percent": 95,
						"reset_at":     "2026-03-27T12:00:00Z",
					},
				},
			},
		},
		Attributes: map[string]string{"path": baseDir + "/codex-user.json"},
	}

	if _, err := store.Save(context.Background(), auth); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	items, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List() len = %d, want 1", len(items))
	}

	payload, ok := items[0].Metadata["codex_quota"].(map[string]any)
	if !ok {
		t.Fatalf("codex_quota metadata missing after round-trip: %#v", items[0].Metadata["codex_quota"])
	}
	rateLimit, ok := payload["rate_limit"].(map[string]any)
	if !ok {
		t.Fatalf("rate_limit missing after round-trip: %#v", payload)
	}
	primary, ok := rateLimit["primary_window"].(map[string]any)
	if !ok {
		t.Fatalf("primary_window missing after round-trip: %#v", rateLimit)
	}
	if got := primary["used_percent"]; got != float64(95) {
		t.Fatalf("primary_window.used_percent = %#v, want 95", got)
	}
	if got := primary["reset_at"]; got != "2026-03-27T12:00:00Z" {
		t.Fatalf("primary_window.reset_at = %#v, want %q", got, "2026-03-27T12:00:00Z")
	}
}
