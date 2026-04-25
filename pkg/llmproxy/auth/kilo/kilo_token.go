// Package kilo provides authentication and token management functionality
// for Kilo AI services.
package kilo

import (
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/base"
)

// KiloTokenStorage stores token information for Kilo AI authentication.
//
// Note: Kilo uses a proprietary token format stored under the "kilocodeToken" JSON key
// rather than the standard "access_token" key, so BaseTokenStorage.AccessToken is not
// populated for this provider.  The Email and Type fields from BaseTokenStorage are used.
type KiloTokenStorage struct {
	base.BaseTokenStorage

	// Token is the Kilo access token (serialised as "kilocodeToken" for Kilo compatibility).
	Token string `json:"kilocodeToken"`

	// OrganizationID is the Kilo organization ID.
	OrganizationID string `json:"kilocodeOrganizationId"`

	// Model is the default model to use.
	Model string `json:"kilocodeModel"`
}

// SaveTokenToFile serializes the Kilo token storage to a JSON file.
func (ts *KiloTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "kilo"
	if err := ts.Save(authFilePath, ts); err != nil {
		return fmt.Errorf("kilo token: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used to persist Kilo credentials.
func CredentialFileName(email string) string {
	return fmt.Sprintf("kilo-%s.json", email)
}
