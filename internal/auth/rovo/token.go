package rovo

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// TokenStorage persists Atlassian Rovo credentials to disk.
// Fields are stored in auth files and later loaded by the core auth manager.
type TokenStorage struct {
	Email   string `json:"email"`
	APIKey  string `json:"api_key"`
	CloudID string `json:"cloud_id"`
	Type    string `json:"type"`

	// Optional metadata for troubleshooting or future use.
	Version string `json:"version,omitempty"`
}

// SaveTokenToFile serializes token storage to disk.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	if ts == nil {
		return fmt.Errorf("rovo token: storage is nil")
	}
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "rovo"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("rovo token: create directory failed: %w", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("rovo token: create file failed: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("rovo token: encode token failed: %w", err)
	}
	return nil
}

// EncodedToken returns base64(email:api_key) used by X-Atlassian-EncodedToken.
func (ts *TokenStorage) EncodedToken() string {
	if ts == nil {
		return ""
	}
	email := strings.TrimSpace(ts.Email)
	apiKey := strings.TrimSpace(ts.APIKey)
	if email == "" || apiKey == "" {
		return ""
	}
	raw := email + ":" + apiKey
	return base64.StdEncoding.EncodeToString([]byte(raw))
}
