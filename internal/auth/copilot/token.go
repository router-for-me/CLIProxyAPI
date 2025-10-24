// Package copilot provides authentication token storage for the Copilot provider.
// It mirrors existing providers' storage layout (claude/codex) to integrate with
// the unified auth manager and management API without special casing.
package copilot

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// TokenStorage stores OAuth2 token information for Copilot authentication.
// The JSON shape intentionally aligns with other providers:
// - includes a provider `type` field ("copilot")
// - persists access/refresh tokens and user email
// - carries an expiry timestamp key named `expired` for consistency
type TokenStorage struct {
    IDToken      string `json:"id_token"`
    AccessToken  string `json:"access_token"`
    RefreshToken string `json:"refresh_token"`
    LastRefresh  string `json:"last_refresh"`
    Email        string `json:"email"`
    Type         string `json:"type"`
    Expire       string `json:"expired"`
    // ExpiresAt persists absolute expiry in unix seconds or milliseconds, as provided by upstream.
    ExpiresAt    int64  `json:"expires_at,omitempty"`
    // RefreshIn persists the server-provided refresh interval in seconds for preemptive refresh.
    RefreshIn    int    `json:"refresh_in,omitempty"`
}

// SaveTokenToFile serializes the Copilot token storage to a JSON file.
// The file content includes `type: "copilot"` to allow management UI and
// registries to identify the provider from the persisted credential.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
    misc.LogSavingCredentials(authFilePath)
    ts.Type = "copilot"
    if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
        return fmt.Errorf("failed to create directory: %v", err)
    }

    f, err := os.Create(authFilePath)
    if err != nil {
        return fmt.Errorf("failed to create token file: %w", err)
    }
    defer func() { _ = f.Close() }()

    if err = json.NewEncoder(f).Encode(ts); err != nil {
        return fmt.Errorf("failed to write token to file: %w", err)
    }
    return nil
}
