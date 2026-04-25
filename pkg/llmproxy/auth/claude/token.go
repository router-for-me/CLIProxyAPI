// Package claude provides authentication and token management functionality
// for Anthropic's Claude AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Claude API.
package claude

import (
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/base"
)

// ClaudeTokenStorage stores OAuth2 token information for Anthropic Claude API authentication.
// It maintains compatibility with the existing auth system while adding Claude-specific fields
// for managing access tokens, refresh tokens, and user account information.
type ClaudeTokenStorage struct {
	base.BaseTokenStorage

	// IDToken is the JWT ID token containing user claims and identity information.
	IDToken string `json:"id_token"`

	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`

	// Expire is the timestamp when the current access token expires.
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the Claude token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *ClaudeTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "claude"
	if err := ts.Save(authFilePath, ts); err != nil {
		return fmt.Errorf("claude token: %w", err)
	}
	return nil
}
