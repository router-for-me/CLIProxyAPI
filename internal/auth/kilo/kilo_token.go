// Package kilo provides authentication and token management functionality
// for Kilo AI services.
package kilo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	log "github.com/sirupsen/logrus"
)

// KiloTokenStorage stores token information for Kilo AI authentication.
type KiloTokenStorage struct {
	// Token is the Kilo access token.
	Token string `json:"kilocodeToken"`

	// OrganizationID is the Kilo organization ID.
	OrganizationID string `json:"kilocodeOrganizationId"`

	// Model is the default model to use.
	Model string `json:"kilocodeModel"`

	// Email is the email address of the authenticated user.
	Email string `json:"email"`

	// Type indicates the authentication provider type, always "kilo" for this storage.
	Type string `json:"type"`
}

// SaveTokenToFile serializes the Kilo token storage to a JSON file.
func (ts *KiloTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "kilo"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("failed to close file: %v", errClose)
		}
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used to persist Kilo credentials.
func CredentialFileName(email string) string {
	return fmt.Sprintf("kilo-%s.json", email)
}
