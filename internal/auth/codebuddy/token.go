// Package codebuddy provides authentication and token management functionality
// for CodeBuddy AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the CodeBuddy API.
package codebuddy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// CodeBuddyTokenStorage stores OAuth token information for CodeBuddy API authentication.
// It maintains compatibility with the existing auth system while adding CodeBuddy-specific fields
// for managing access tokens and user account information.
type CodeBuddyTokenStorage struct {
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token used to obtain new access tokens.
	RefreshToken string `json:"refresh_token"`
	// ExpiresIn is the number of seconds until the access token expires.
	ExpiresIn int64 `json:"expires_in"`
	// RefreshExpiresIn is the number of seconds until the refresh token expires.
	RefreshExpiresIn int64 `json:"refresh_expires_in,omitempty"`
	// TokenType is the type of token, typically "bearer".
	TokenType string `json:"token_type"`
	// Domain is the CodeBuddy service domain/region.
	Domain string `json:"domain"`
	// UserID is the user ID associated with this token.
	UserID string `json:"user_id"`
	// Type indicates the authentication provider type, always "codebuddy" for this storage.
	Type string `json:"type"`
}

// SaveTokenToFile serializes the CodeBuddy token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (s *CodeBuddyTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	s.Type = "codebuddy"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	f, err := os.OpenFile(authFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(s); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
