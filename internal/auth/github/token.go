package github

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// GithubTokenStorage stores credentials for GitHub Copilot authentication.
// It holds both the long-lived GitHub user token and the short-lived Copilot API token.
type GithubTokenStorage struct {
	// GithubToken is the long-lived GitHub user OAuth token (ghu_...).
	// Used to refresh the Copilot API token when it expires.
	GithubToken string `json:"github_token"`
	// AccessToken is the short-lived GitHub Copilot API token (tid=...).
	// Used as the Bearer token for Copilot API requests.
	AccessToken string `json:"access_token"`
	// Login is the GitHub username of the authenticated user.
	Login string `json:"login"`
	// Email is the GitHub account email, used for file naming and display.
	Email string `json:"email,omitempty"`
	// Expired is the RFC3339 timestamp when the Copilot access token expires.
	Expired string `json:"expired,omitempty"`
	// Type identifies the authentication provider, always "github-copilot".
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// Not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata before saving.
func (ts *GithubTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the GitHub Copilot token storage to a JSON file.
func (ts *GithubTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "github-copilot"

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

// DeviceCodeResponse holds the response from the device code endpoint.
type DeviceCodeResponse struct {
	// DeviceCode is the opaque device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user completes authorization.
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete is the URL with the user code pre-filled.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum polling interval in seconds.
	Interval int `json:"interval"`
}

// CopilotToken holds the Copilot-specific API token returned by GitHub.
type CopilotToken struct {
	// Token is the Copilot API bearer token (e.g. "tid=...").
	Token string `json:"token"`
	// ExpiresAt is the expiry of the Copilot token. GitHub returns this as a
	// Unix timestamp (number), so we use json.Number to handle both forms.
	ExpiresAt json.Number `json:"expires_at"`
}
