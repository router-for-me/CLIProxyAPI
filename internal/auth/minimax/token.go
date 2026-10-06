package minimax

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	log "github.com/sirupsen/logrus"
)

// MinimaxTokenStorage stores OAuth2 credentials for the MiniMax platform.
type MinimaxTokenStorage struct {
	// AccessToken authenticates platform API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken renews the access token.
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType is the OAuth token type, typically "Bearer".
	TokenType string `json:"token_type,omitempty"`
	// Scope is the granted OAuth scope list.
	Scope string `json:"scope,omitempty"`
	// Region is the MiniMax region the credential belongs to.
	Region string `json:"region,omitempty"`
	// Expired is the RFC3339 timestamp at which the access token expires.
	Expired string `json:"expired,omitempty"`
	// LastRefresh records the last successful refresh in RFC3339.
	LastRefresh string `json:"last_refresh,omitempty"`
	// ResourceURL is the resource URL returned by the authorization server.
	ResourceURL string `json:"resource_url,omitempty"`
	// BaseURL is the API base URL resolved for this credential.
	BaseURL string `json:"base_url,omitempty"`
	// Type is the provider key stored in the credential file.
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks. It is not
	// exported directly so it can be flattened during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata before saving.
func (ts *MinimaxTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the token storage to a JSON credential file.
func (ts *MinimaxTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	if ts.Type == "" {
		ts.Type = "minimax"
	}
	ts.Region = NormalizeRegion(ts.Region)
	if ts.BaseURL == "" {
		ts.BaseURL = ResolveAPIBaseURL(ts.Region)
	}

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("minimax: failed to create directory: %w", err)
	}

	data, err := misc.MergeMetadata(ts, ts.Metadata)
	if err != nil {
		return fmt.Errorf("minimax: failed to merge metadata: %w", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("minimax: failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("minimax token storage: close token file error: %v", errClose)
		}
	}()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("minimax: failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired reports whether the access token is expired or nearly expired.
func (ts *MinimaxTokenStorage) IsExpired() bool {
	if ts == nil || strings.TrimSpace(ts.Expired) == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true
	}
	return time.Now().Add(refreshThreshold).After(t)
}

// NeedsRefresh reports whether the token should be refreshed before use.
func (ts *MinimaxTokenStorage) NeedsRefresh() bool {
	if ts == nil || ts.RefreshToken == "" {
		return false
	}
	return ts.IsExpired()
}
