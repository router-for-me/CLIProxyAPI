// Package copilot provides authentication and token management functionality
// for GitHub Copilot AI services. It handles OAuth2 device flow token storage,
// serialization, and retrieval for maintaining authenticated sessions with the Copilot API.
package copilot

import (
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/base"
)

// CopilotTokenStorage stores OAuth2 token information for GitHub Copilot API authentication.
// It maintains compatibility with the existing auth system while adding Copilot-specific fields
// for managing access tokens and user account information.
type CopilotTokenStorage struct {
	base.BaseTokenStorage

	// TokenType is the type of token, typically "bearer".
	TokenType string `json:"token_type"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope"`
	// ExpiresAt is the timestamp when the access token expires (if provided).
	ExpiresAt string `json:"expires_at,omitempty"`
	// Username is the GitHub username associated with this token.
	Username string `json:"username"`
}

// CopilotTokenData holds the raw OAuth token response from GitHub.
type CopilotTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"access_token"`
	// TokenType is the type of token, typically "bearer".
	TokenType string `json:"token_type"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope"`
}

// CopilotAuthBundle bundles authentication data for storage.
type CopilotAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *CopilotTokenData
	// Username is the GitHub username.
	Username string
}

// DeviceCodeResponse represents GitHub's device code response.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user should enter the code.
	VerificationURI string `json:"verification_uri"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
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
	ts.Type = "github-copilot"
	if err := ts.Save(authFilePath, ts); err != nil {
		return fmt.Errorf("copilot token: %w", err)
	}
	return nil
}
