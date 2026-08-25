// Package cursor provides authentication and token management for Cursor.
// It drives the deep-link login flow used by the Cursor editor: the proxy builds
// an authorization URL, the user approves it in a browser, and the proxy polls
// Cursor's auth endpoint until the callback has been validated and a token is
// issued.
package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// CursorTokenStorage stores the credentials issued by the Cursor login flow.
//
// Cursor issues a long-lived access token and no refresh token, so there is no
// expiry bookkeeping here: a rejected token has to be replaced by logging in again.
type CursorTokenStorage struct {
	// AccessToken is the raw Cursor access token.
	AccessToken string `json:"access_token"`
	// AuthID is the auth identity reported by Cursor ("provider|userId").
	AuthID string `json:"auth_id,omitempty"`
	// Cookie is the ready-to-use Cursor cookie ("userId%3A%3AaccessToken").
	Cookie string `json:"cookie,omitempty"`
	// Type indicates the authentication provider type, always "cursor" here.
	Type string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *CursorTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Cursor token storage to a JSON file.
func (ts *CursorTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "cursor"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
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
			log.Errorf("cursor token storage: close token file error: %v", errClose)
		}
	}()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// LoginResult is the outcome of a completed Cursor login flow.
type LoginResult struct {
	// AccessToken is the raw Cursor access token.
	AccessToken string
	// AuthID is the auth identity ("provider|userId").
	AuthID string
	// Cookie is the ready-to-use Cursor cookie:
	// "userId%3A%3AaccessToken" when AuthID carries a user id, otherwise the
	// bare access token.
	Cookie string
}

// CursorAuthBundle bundles authentication data for storage.
type CursorAuthBundle struct {
	// Login carries the tokens returned by the login flow.
	Login *LoginResult
}

// Label returns a human-readable account label derived from the auth identity.
func (b *CursorAuthBundle) Label() string {
	if b == nil || b.Login == nil {
		return "Cursor User"
	}
	if parts := strings.SplitN(b.Login.AuthID, "|", 2); len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		return "Cursor User (" + strings.TrimSpace(parts[1]) + ")"
	}
	return "Cursor User"
}
