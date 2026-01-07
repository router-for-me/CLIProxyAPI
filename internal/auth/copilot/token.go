// Package copilot provides authentication and token management functionality
// for GitHub Copilot API. It handles OAuth2 device flow token storage, serialization,
// and retrieval for maintaining authenticated sessions with the GitHub Copilot API.
package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// CopilotTokenStorage stores OAuth2 token information for GitHub Copilot API authentication.
// It maintains both the GitHub access token and the derived Copilot token with their
// respective expiration times.
type CopilotTokenStorage struct {
	// GitHubToken is the GitHub OAuth access token (ghu_xxx) used to exchange for Copilot tokens.
	GitHubToken string `json:"github_token"`

	// CopilotToken is the Copilot API token obtained from GitHub token exchange.
	// This token expires approximately every 25 minutes and needs to be refreshed.
	CopilotToken string `json:"copilot_token"`

	// CopilotAPIBase is the base URL for the Copilot API endpoints.
	CopilotAPIBase string `json:"copilot_api_base"`

	// CopilotExpire is the timestamp when the Copilot token expires.
	CopilotExpire string `json:"copilot_expire"`

	// LastRefresh is the timestamp of the last Copilot token refresh operation.
	LastRefresh string `json:"last_refresh"`

	// Email is the GitHub account email address associated with this token.
	Email string `json:"email,omitempty"`

	// Type indicates the authentication provider type, always "copilot" for this storage.
	Type string `json:"type"`

	// SKU is the Copilot subscription type (e.g., "free_educational_quota", "copilot_individual").
	SKU string `json:"sku,omitempty"`
}

// SaveTokenToFile serializes the Copilot token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *CopilotTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "copilot"

	// Create directory structure if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	// Create the token file
	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	// Encode and write the token data as JSON
	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
