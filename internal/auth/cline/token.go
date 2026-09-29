package cline

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

// ClineTokenStorage stores Cline OAuth credentials on disk.
// AuthKind: "oauth". Refresh tokens rotate; the stored refresh token must be
// replaced on every successful refresh.
type ClineTokenStorage struct {
	// Type indicates the authentication provider type ("cline").
	Type string `json:"type"`
	// AccessToken is the Cline account access token used for API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is the Cline account refresh token; it rotates on refresh.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the token type, typically "Bearer".
	TokenType string `json:"token_type,omitempty"`
	// Expired is the RFC3339 timestamp when the access token expires.
	Expired string `json:"expired,omitempty"`
	// LastRefresh is the RFC3339 timestamp of the last successful refresh.
	LastRefresh string `json:"last_refresh,omitempty"`
	// Email is the account email reported by the Cline account API.
	Email string `json:"email,omitempty"`
	// Name is the account display name.
	Name string `json:"name,omitempty"`
	// Subject is the WorkOS subject identifier.
	Subject string `json:"sub,omitempty"`
	// BaseURL is the base URL for API requests.
	BaseURL string `json:"base_url,omitempty"`

	// Models is the per-account detected model catalog fetched at login (or on
	// first import). Persisted so only models the account can use are advertised.
	Models []ClineModelInfo `json:"models,omitempty"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows the token store to merge status fields before saving.
func (ts *ClineTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// CreateTokenStorage converts a token record into persistable storage.
func (a *ClineAuth) CreateTokenStorage(bundle *ClineAuthBundle) *ClineTokenStorage {
	if bundle == nil || bundle.TokenRecord == nil {
		return nil
	}
	storage := &ClineTokenStorage{
		Type:         "cline",
		AccessToken:  bundle.TokenRecord.AccessToken,
		RefreshToken: bundle.TokenRecord.RefreshToken,
		TokenType:    bundle.TokenRecord.TokenType,
		Expired:      bundle.TokenRecord.ExpiresAt,
		LastRefresh:  bundle.LastRefresh,
		BaseURL:      DefaultAPIBaseURL,
		Models:       bundle.Models,
	}
	if bundle.TokenRecord.UserInfo != nil {
		storage.Email = strings.TrimSpace(bundle.TokenRecord.UserInfo.Email)
		storage.Name = strings.TrimSpace(bundle.TokenRecord.UserInfo.Name)
		storage.Subject = strings.TrimSpace(bundle.TokenRecord.UserInfo.Subject)
	}
	return storage
}

// SaveTokenToFile serializes the Cline token storage to a JSON file.
func (ts *ClineTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	if ts.Type == "" {
		ts.Type = "cline"
	}
	if ts.BaseURL == "" {
		ts.BaseURL = DefaultAPIBaseURL
	}

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("cline token storage: create directory: %w", err)
	}

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("cline token storage: merge metadata: %w", errMerge)
	}

	file, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("cline token storage: create token file: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Errorf("cline token storage: close token file error: %v", errClose)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("cline token storage: write token file: %w", err)
	}
	return nil
}

// IsExpired checks if the token has expired.
func (ts *ClineTokenStorage) IsExpired() bool {
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
func (ts *ClineTokenStorage) NeedsRefresh() bool {
	if ts.RefreshToken == "" {
		return false // Can't refresh without refresh token
	}
	return ts.IsExpired()
}

// CredentialFileName returns the filename used for Cline credentials.
func CredentialFileName(email, subject string) string {
	email = sanitizeFileSegment(email)
	if email != "" {
		return fmt.Sprintf("cline-%s.json", email)
	}
	subject = sanitizeFileSegment(subject)
	if subject != "" {
		return fmt.Sprintf("cline-%s.json", subject)
	}
	return fmt.Sprintf("cline-%d.json", time.Now().UnixMilli())
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
