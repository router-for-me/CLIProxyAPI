package xai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	log "github.com/sirupsen/logrus"
)

// TokenStorage stores xAI OAuth credentials on disk.
type TokenStorage struct {
	Type          string `json:"type"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	IDToken       string `json:"id_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	ExpiresIn     int    `json:"expires_in,omitempty"`
	Expire        string `json:"expired,omitempty"`
	LastRefresh   string `json:"last_refresh,omitempty"`
	Email         string `json:"email,omitempty"`
	Subject       string `json:"sub,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	RedirectURI   string `json:"redirect_uri,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	AuthKind      string `json:"auth_kind,omitempty"`

	Metadata map[string]any `json:"-"`
}

// SetMetadata allows the token store to merge status fields before saving.
func (ts *TokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile writes xAI credentials to a JSON auth file.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "xai"
	ts.AuthKind = "oauth"
	if errMkdirAll := os.MkdirAll(filepath.Dir(authFilePath), 0o700); errMkdirAll != nil {
		return fmt.Errorf("xai token storage: create directory: %w", errMkdirAll)
	}
	file, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("xai token storage: create token file: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Errorf("xai token storage: close token file error: %v", errClose)
		}
	}()

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("xai token storage: merge metadata: %w", errMerge)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("xai token storage: write token file: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used for xAI credentials.
func CredentialFileName(email, subject string) string {
	email = sanitizeFileSegment(email)
	if email != "" {
		return fmt.Sprintf("xai-%s.json", email)
	}
	subject = sanitizeFileSegment(subject)
	if subject != "" {
		return fmt.Sprintf("xai-%s.json", subject)
	}
	return fmt.Sprintf("xai-%d.json", time.Now().UnixMilli())
}

func sanitizeFileSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '@' || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
