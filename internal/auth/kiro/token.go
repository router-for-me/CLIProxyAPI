package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	log "github.com/sirupsen/logrus"
)

// TokenStorage stores Kiro OAuth credentials on disk.
type TokenStorage struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProfileArn   string `json:"profile_arn,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Expired      string `json:"expired,omitempty"`
	Email        string `json:"email,omitempty"`
	AuthMethod   string `json:"auth_method,omitempty"`
	Provider     string `json:"provider,omitempty"`
	AuthKind     string `json:"auth_kind,omitempty"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata allows the token store to merge status fields before saving.
func (ts *TokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile writes Kiro credentials to a JSON auth file.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "kiro"
	ts.AuthKind = "oauth"
	if errMkdirAll := os.MkdirAll(filepath.Dir(authFilePath), 0o700); errMkdirAll != nil {
		return fmt.Errorf("kiro token storage: create directory: %w", errMkdirAll)
	}

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("kiro token storage: merge metadata: %w", errMerge)
	}

	file, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("kiro token storage: create token file: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Errorf("kiro token storage: close token file error: %v", errClose)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("kiro token storage: encode token data: %w", err)
	}
	return nil
}
