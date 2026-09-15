package zcode

import (
	"encoding/json"
	"os"
)

// TokenStorage persists the static ZCode credential plus the stable device id.
// Headers and BaseURL are persisted so the native identity-header set and the
// anthropic endpoint survive a server restart (Attributes are in-memory only and
// are reconstructed from this file via ApplyCustomHeadersFromMetadata).
type TokenStorage struct {
	APIKey    string            `json:"api_key"`
	Secret    string            `json:"secret,omitempty"`
	JWT       string            `json:"jwt,omitempty"`
	UserID    string            `json:"user_id,omitempty"`
	DeviceMid string            `json:"device_mid,omitempty"`
	Provider  string            `json:"type"`
	BaseURL   string            `json:"base_url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata satisfies the cliproxy TokenStorage hook.
func (ts *TokenStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

// FullCredential returns the resolved Credential view.
func (ts *TokenStorage) FullCredential() *Credential {
	return &Credential{APIKey: ts.APIKey, Secret: ts.Secret, JWT: ts.JWT, UserID: ts.UserID}
}

// SaveTokenToFile persists the storage as JSON.
func (ts *TokenStorage) SaveTokenToFile(path string) error {
	raw, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// LoadTokenFromFile restores the storage from JSON.
func (ts *TokenStorage) LoadTokenFromFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, ts)
}
