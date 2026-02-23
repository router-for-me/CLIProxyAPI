// Package qwen provides authentication and token management functionality
// for Alibaba's Qwen AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Qwen API.
package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/misc"
)

// QwenTokenStorage stores OAuth2 token information for Alibaba Qwen API authentication.
// It maintains compatibility with the existing auth system while adding Qwen-specific fields
// for managing access tokens, refresh tokens, and user account information.
type QwenTokenStorage struct {
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`
	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`
	// ResourceURL is the base URL for API requests.
	ResourceURL string `json:"resource_url"`
	// Email is the Qwen account email address associated with this token.
	Email string `json:"email"`
	// Type indicates the authentication provider type, always "qwen" for this storage.
	Type string `json:"type"`
	// Expire is the timestamp when the current access token expires.
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the Qwen token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *QwenTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "qwen"
	path, err := validateTokenFilePath(authFilePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(path)
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

func validateTokenFilePath(authFilePath string) (string, error) {
	trimmed := strings.TrimSpace(authFilePath)
	if trimmed == "" {
		return "", fmt.Errorf("failed to write token: auth file path is empty")
	}
	segments := strings.Split(filepath.ToSlash(trimmed), "/")
	for _, segment := range segments {
		if segment == ".." {
			return "", fmt.Errorf("failed to write token: auth file path traversal is not allowed")
		}
	}
	clean := filepath.Clean(trimmed)
	if clean == "." {
		return "", fmt.Errorf("failed to write token: auth file path is invalid")
	}
	return clean, nil
}
