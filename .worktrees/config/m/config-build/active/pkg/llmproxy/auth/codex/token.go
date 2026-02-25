// Package codex provides authentication and token management functionality
// for OpenAI's Codex AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Codex API.
package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/misc"
)

func sanitizeTokenFilePath(authFilePath string) (string, error) {
	trimmed := strings.TrimSpace(authFilePath)
	if trimmed == "" {
		return "", fmt.Errorf("token file path is empty")
	}
	cleaned := filepath.Clean(trimmed)
	parts := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, part := range parts {
		if part == ".." {
			return "", fmt.Errorf("invalid token file path")
		}
	}
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("failed to resolve token file path: %w", err)
	}
	return absPath, nil
}

// CodexTokenStorage stores OAuth2 token information for OpenAI Codex API authentication.
// It maintains compatibility with the existing auth system while adding Codex-specific fields
// for managing access tokens, refresh tokens, and user account information.
type CodexTokenStorage struct {
	// IDToken is the JWT ID token containing user claims and identity information.
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`
	// AccountID is the OpenAI account identifier associated with this token.
	AccountID string `json:"account_id"`
	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`
	// Email is the OpenAI account email address associated with this token.
	Email string `json:"email"`
	// Type indicates the authentication provider type, always "codex" for this storage.
	Type string `json:"type"`
	// Expire is the timestamp when the current access token expires.
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the Codex token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *CodexTokenStorage) SaveTokenToFile(authFilePath string) error {
	safePath, err := misc.ResolveSafeFilePath(authFilePath)
	if err != nil {
		return fmt.Errorf("invalid token file path: %w", err)
	}
	misc.LogSavingCredentials(safePath)
	ts.Type = "codex"
	if err = os.MkdirAll(filepath.Dir(safePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(safePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil

}
