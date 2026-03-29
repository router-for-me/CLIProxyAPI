// Package qoder provides authentication and token handling for Qoder API.
package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// QoderTokenStorage stores OAuth2 token information for Qoder API authentication.
// It maintains compatibility with the existing auth system while adding Qoder-specific fields.
type QoderTokenStorage struct {
	// Token is the OAuth2 access token used for authenticating API requests.
	Token string `json:"token"`
	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`
	// UserID is the unique identifier for the Qoder user.
	UserID string `json:"user_id"`
	// Name is the user's display name.
	Name string `json:"name"`
	// Email is the Qoder account email address associated with this token.
	Email string `json:"email"`
	// ExpireTime is the timestamp when the current access token expires (milliseconds epoch).
	ExpireTime int64 `json:"expire_time"`
	// Type indicates the authentication provider type, always "qoder" for this storage.
	Type string `json:"type"`
	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`
	// MachineID is the persistent machine identifier for this installation.
	MachineID string `json:"machine_id,omitempty"`
	// MachineToken is the machine-specific token (if returned by auth server).
	MachineToken string `json:"machine_token,omitempty"`
	// MachineType is the type of machine registration.
	MachineType string `json:"machine_type,omitempty"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *QoderTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Qoder token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
// It merges any injected metadata into the top-level JSON object.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *QoderTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "qoder"

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

	// Merge metadata using helper
	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	if err = json.NewEncoder(f).Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired checks if the token has expired or will expire within the given duration
func (ts *QoderTokenStorage) IsExpired(bufferDuration int64) bool {
	if ts.ExpireTime == 0 {
		return true
	}
	now := time.Now().UnixMilli()
	return ts.ExpireTime-now-bufferDuration <= 0
}
