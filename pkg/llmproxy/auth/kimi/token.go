// Package kimi provides authentication and token management functionality
// for Kimi (Moonshot AI) services. It handles OAuth2 device flow token storage,
// serialization, and retrieval for maintaining authenticated sessions with the Kimi API.
package kimi

import (
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/base"
	"time"
)

// KimiTokenStorage stores OAuth2 token information for Kimi API authentication.
type KimiTokenStorage struct {
	base.BaseTokenStorage

	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope,omitempty"`
	// DeviceID is the OAuth device flow identifier used for Kimi requests.
	DeviceID string `json:"device_id,omitempty"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
}

// KimiTokenData holds the raw OAuth token response from Kimi.
type KimiTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expires_at"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope"`
}

// KimiAuthBundle bundles authentication data for storage.
type KimiAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *KimiTokenData
	// DeviceID is the device identifier used during OAuth device flow.
	DeviceID string
}

// DeviceCodeResponse represents Kimi's device code response.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user should enter the code.
	VerificationURI string `json:"verification_uri,omitempty"`
	// VerificationURIComplete is the URL with the code pre-filled.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
}

// SaveTokenToFile serializes the Kimi token storage to a JSON file.
func (ts *KimiTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "kimi"
	if err := ts.Save(authFilePath, ts); err != nil {
		return fmt.Errorf("kimi token: %w", err)
	}
	return nil
}

// IsExpired checks if the token has expired.
func (ts *KimiTokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false // No expiry set, assume valid
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true // Has expiry string but can't parse
	}
	// Consider expired if within refresh threshold
	return time.Now().Add(time.Duration(refreshThresholdSeconds) * time.Second).After(t)
}

// NeedsRefresh checks if the token should be refreshed.
func (ts *KimiTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false // Can't refresh without refresh token
	}
	return ts.IsExpired()
}
