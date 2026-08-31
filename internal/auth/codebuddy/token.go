package codebuddy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// TokenStorage stores OAuth credentials and account metadata for CodeBuddy.
type TokenStorage struct {
	// AccessToken is the bearer token used for API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain a new access token.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the token type, typically "Bearer".
	TokenType string `json:"token_type,omitempty"`
	// Scope is the authorization scope granted to the token.
	Scope string `json:"scope,omitempty"`
	// Domain is the user domain identifier returned by the token endpoint.
	Domain string `json:"domain,omitempty"`
	// UID is the account user id, used as the X-User-Id header value.
	UID string `json:"uid,omitempty"`
	// Nickname is the account display name.
	Nickname string `json:"nickname,omitempty"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
	// Type indicates the provider, always "codebuddy-cn" for this storage.
	Type string `json:"type"`
	// EnabledModels lists the model IDs the account can call, synced from /v3/config.
	EnabledModels []string `json:"enabled_models,omitempty"`
	// ModelsMeta stores the parsed model metadata JSON from /v3/config.
	ModelsMeta string `json:"models_meta,omitempty"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *TokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "codebuddy-cn"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("codebuddy token storage: close token file error: %v", errClose)
		}
	}()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// IsExpired checks whether the access token is expired or close to expiring.
func (ts *TokenStorage) IsExpired() bool {
	if ts.Expired == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts.Expired)
	if err != nil {
		return true
	}
	return time.Now().After(t)
}
