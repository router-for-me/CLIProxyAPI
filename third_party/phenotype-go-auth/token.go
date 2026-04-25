// Package auth provides shared authentication and token management functionality
// for Phenotype services. It includes token storage interfaces, token persistence,
// and OAuth2 helper utilities.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TokenStorage defines the interface for token persistence and retrieval.
// Implementations should handle secure storage of OAuth2 tokens and related metadata.
type TokenStorage interface {
	// Load reads the token from storage (typically a file).
	// Returns the token data or an error if loading fails.
	Load() error

	// Save writes the token to storage (typically a file).
	// Returns an error if saving fails.
	Save() error

	// Clear removes the token from storage.
	// Returns an error if clearing fails.
	Clear() error

	// GetAccessToken returns the current access token.
	GetAccessToken() string

	// GetRefreshToken returns the current refresh token.
	GetRefreshToken() string

	// GetIDToken returns the current ID token.
	GetIDToken() string

	// GetEmail returns the email associated with this token.
	GetEmail() string

	// GetType returns the provider type (e.g., "claude", "github-copilot").
	GetType() string

	// GetMetadata returns arbitrary metadata associated with this token.
	GetMetadata() map[string]any

	// SetMetadata allows external callers to inject metadata before saving.
	SetMetadata(meta map[string]any)
}

// BaseTokenStorage provides a shared implementation of token storage
// with common fields used across all OAuth2 providers.
type BaseTokenStorage struct {
	// IDToken is the JWT ID token containing user claims and identity information.
	IDToken string `json:"id_token"`

	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`

	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`

	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`

	// Email is the email address associated with this token.
	Email string `json:"email"`

	// Type indicates the authentication provider type (e.g., "claude", "github-copilot").
	Type string `json:"type"`

	// Expire is the timestamp when the current access token expires.
	Expire string `json:"expired"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`

	// filePath is the path where the token is stored.
	filePath string
}

// NewBaseTokenStorage creates a new BaseTokenStorage instance with the given file path.
//
// Parameters:
//   - filePath: The full path where the token file should be saved/loaded
//
// Returns:
//   - *BaseTokenStorage: A new BaseTokenStorage instance
func NewBaseTokenStorage(filePath string) *BaseTokenStorage {
	return &BaseTokenStorage{
		filePath: filePath,
		Metadata: make(map[string]any),
	}
}

// Load reads the token from the file path.
// Returns an error if the operation fails or the file does not exist.
func (ts *BaseTokenStorage) Load() error {
	filePath := strings.TrimSpace(ts.filePath)
	if filePath == "" {
		return fmt.Errorf("token file path is empty")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read token file: %w", err)
	}

	if err = json.Unmarshal(data, ts); err != nil {
		return fmt.Errorf("failed to parse token file: %w", err)
	}

	return nil
}

// Save writes the token to the file path.
// Creates the necessary directory structure if it doesn't exist.
func (ts *BaseTokenStorage) Save() error {
	filePath := strings.TrimSpace(ts.filePath)
	if filePath == "" {
		return fmt.Errorf("token file path is empty")
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Merge metadata into the token data for JSON serialization
	data := ts.toJSONMap()

	// Write to file
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token: %w", err)
	}

	if err := os.WriteFile(filePath, jsonData, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}

// Clear removes the token file.
// Returns nil if the file doesn't exist.
func (ts *BaseTokenStorage) Clear() error {
	filePath := strings.TrimSpace(ts.filePath)
	if filePath == "" {
		return fmt.Errorf("token file path is empty")
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove token file: %w", err)
	}

	return nil
}

// GetAccessToken returns the access token.
func (ts *BaseTokenStorage) GetAccessToken() string {
	return ts.AccessToken
}

// GetRefreshToken returns the refresh token.
func (ts *BaseTokenStorage) GetRefreshToken() string {
	return ts.RefreshToken
}

// GetIDToken returns the ID token.
func (ts *BaseTokenStorage) GetIDToken() string {
	return ts.IDToken
}

// GetEmail returns the email.
func (ts *BaseTokenStorage) GetEmail() string {
	return ts.Email
}

// GetType returns the provider type.
func (ts *BaseTokenStorage) GetType() string {
	return ts.Type
}

// GetMetadata returns the metadata.
func (ts *BaseTokenStorage) GetMetadata() map[string]any {
	return ts.Metadata
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *BaseTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// UpdateLastRefresh updates the LastRefresh timestamp to the current time.
func (ts *BaseTokenStorage) UpdateLastRefresh() {
	ts.LastRefresh = time.Now().UTC().Format(time.RFC3339)
}

// IsExpired checks if the token has expired based on the Expire timestamp.
func (ts *BaseTokenStorage) IsExpired() bool {
	if ts.Expire == "" {
		return false
	}

	expireTime, err := time.Parse(time.RFC3339, ts.Expire)
	if err != nil {
		return false
	}

	return time.Now().After(expireTime)
}

// toJSONMap converts the token storage to a map for JSON serialization,
// merging in any metadata.
func (ts *BaseTokenStorage) toJSONMap() map[string]any {
	result := map[string]any{
		"id_token":      ts.IDToken,
		"access_token":  ts.AccessToken,
		"refresh_token": ts.RefreshToken,
		"last_refresh":  ts.LastRefresh,
		"email":         ts.Email,
		"type":          ts.Type,
		"expired":       ts.Expire,
	}

	// Merge metadata into the result
	for key, value := range ts.Metadata {
		if key != "" {
			result[key] = value
		}
	}

	return result
}
