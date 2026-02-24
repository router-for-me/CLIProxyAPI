package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// KiroTokenStorage holds the persistent token data for Kiro authentication.
type KiroTokenStorage struct {
	// Type is the provider type for management UI recognition (must be "kiro")
	Type string `json:"type"`
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// ProfileArn is the AWS CodeWhisperer profile ARN
	ProfileArn string `json:"profile_arn"`
	// ExpiresAt is the timestamp when the token expires
	ExpiresAt string `json:"expires_at"`
	// AuthMethod indicates the authentication method used
	AuthMethod string `json:"auth_method"`
	// Provider indicates the OAuth provider
	Provider string `json:"provider"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
	// ClientID is the OAuth client ID (required for token refresh)
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret is the OAuth client secret (required for token refresh)
	ClientSecret string `json:"client_secret,omitempty"`
	// Region is the AWS region
	Region string `json:"region,omitempty"`
	// StartURL is the AWS Identity Center start URL (for IDC auth)
	StartURL string `json:"start_url,omitempty"`
	// Email is the user's email address
	Email string `json:"email,omitempty"`
}

// SaveTokenToFile persists the token storage to the specified file path.
func (s *KiroTokenStorage) SaveTokenToFile(authFilePath string) error {
	dir := filepath.Dir(authFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token storage: %w", err)
	}

	if err := os.WriteFile(authFilePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}

// LoadFromFile loads token storage from the specified file path.
func LoadFromFile(authFilePath string) (*KiroTokenStorage, error) {
	data, err := os.ReadFile(authFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var storage KiroTokenStorage
	if err := json.Unmarshal(data, &storage); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &storage, nil
}

// ToTokenData converts storage to KiroTokenData for API use.
func (s *KiroTokenStorage) ToTokenData() *KiroTokenData {
	return &KiroTokenData{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ProfileArn:   s.ProfileArn,
		ExpiresAt:    s.ExpiresAt,
		AuthMethod:   s.AuthMethod,
		Provider:     s.Provider,
		ClientID:     s.ClientID,
		ClientSecret: s.ClientSecret,
		Region:       s.Region,
		StartURL:     s.StartURL,
		Email:        s.Email,
	}
}
