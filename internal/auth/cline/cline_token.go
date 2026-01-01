/**
 * @file Token storage implementation for Cline authentication
 * @description Provides persistent storage for Cline OAuth tokens including access tokens,
 * refresh tokens, and user account information. Implements the TokenStorage interface
 * for seamless integration with the authentication system. Tokens are stored in JSON format
 * with proper directory structure and file permissions for security.
 */

package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// ClineTokenStorage stores OAuth2 token information for Cline API authentication.
// It maintains compatibility with the existing auth system while adding Cline-specific fields
// for managing access tokens, refresh tokens, and user account information.
type ClineTokenStorage struct {
	// AccessToken is the JWT token used for authenticating API requests
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens when the current one expires
	RefreshToken string `json:"refresh_token"`
	// LastRefresh is the timestamp of the last token refresh operation
	LastRefresh string `json:"last_refresh"`
	// Email is the Cline account email address associated with this token
	Email string `json:"email"`
	// Type indicates the authentication provider type, always "cline" for this storage
	Type string `json:"type"`
	// Expire is the timestamp when the current access token expires
	Expire string `json:"expired"`
}

// SaveTokenToFile serializes the Cline token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *ClineTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "cline"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
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
