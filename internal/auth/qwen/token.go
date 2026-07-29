// Package qwen provides authentication and token management for the Qianwen
// platform (Alibaba Cloud Bailian) usage/billing API. It implements the OAuth2
// device authorization grant flow used by `qianwen login` to obtain an access
// token that can query account usage and quota.
//
// NOTE: This OAuth credential is used ONLY for usage/quota queries against the
// Qianwen gateway (cli.qianwenai.com). Model inference uses separate DashScope
// API keys (sk-sp-...) configured via qwen-api-key and is handled elsewhere.
package qwen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// QwenTokenStorage stores OAuth2 token information for Qianwen API authentication.
type QwenTokenStorage struct {
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token (may be empty; Qianwen has no
	// public refresh endpoint, expiry is handled by re-login).
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType is the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// DeviceID is the persistent device identifier (client_id) used during the flow.
	DeviceID string `json:"device_id,omitempty"`
	// Email is the authorized account email, when provided.
	Email string `json:"email,omitempty"`
	// AliyunID is the authorized Aliyun account identifier, when provided.
	AliyunID string `json:"aliyun_id,omitempty"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
	// Type indicates the authentication provider type, always "qianwen" for this storage.
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *QwenTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// QwenTokenData holds the raw OAuth token response from Qianwen.
type QwenTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth2 refresh token.
	RefreshToken string `json:"refresh_token"`
	// ExpiresAt is the RFC3339 timestamp when the token expires.
	ExpiresAt string `json:"expires_at"`
	// Email is the authorized account email.
	Email string `json:"email"`
	// AliyunID is the authorized Aliyun account identifier.
	AliyunID string `json:"aliyun_id"`
}

// QwenAuthBundle bundles authentication data for storage.
type QwenAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *QwenTokenData
	// DeviceID is the device identifier used during OAuth device flow.
	DeviceID string
}

// DeviceCodeResponse represents Qianwen's device code response.
type DeviceCodeResponse struct {
	// Token is the device flow polling token.
	Token string `json:"token"`
	// VerificationURL is the URL where the user authorizes the device.
	VerificationURL string `json:"verification_url"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
	// CodeVerifier is the PKCE verifier preserved from init and sent when polling.
	CodeVerifier string `json:"-"`
}

// SaveTokenToFile serializes the Qwen token storage to a JSON file.
func (ts *QwenTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "qianwen"

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

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired checks if the token has expired.
func (ts *QwenTokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false // No expiry set, assume valid
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true // Has expiry string but can't parse
	}
	return time.Now().Add(time.Duration(refreshThresholdSeconds) * time.Second).After(t)
}

// NeedsRefresh checks if the token should be refreshed. Qianwen has no public
// refresh endpoint, so this reports false; expired tokens require re-login.
func (ts *QwenTokenStorage) NeedsRefresh() bool {
	return false
}
