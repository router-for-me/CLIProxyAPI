// Package zai provides authentication and token management for Z.AI / ZCode (GLM)
// coding plans. It handles the ZCode CLI OAuth flow: a poll-based authorization
// grant that mints a coding-plan token usable against an Anthropic-compatible
// endpoint, serialization of the resulting credentials, and retrieval for
// maintaining authenticated sessions.
package zai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// TokenStorage stores Z.AI / ZCode coding-plan credentials on disk.
//
// The OAuth flow mints a long-lived coding-plan token (AccessToken) that is sent
// as a Bearer token to the Anthropic-compatible endpoint (BaseURL). The flow does
// not return a refresh token; when the token is rejected the user logs in again.
type TokenStorage struct {
	// Type indicates the authentication provider type, always "zai" for this storage.
	Type string `json:"type"`
	// Provider is the identity provider used to authenticate: "zai" (international)
	// or "bigmodel" (Zhipu BigModel, China mainland).
	Provider string `json:"provider,omitempty"`
	// AccessToken is the minted coding-plan API key ("apiKey.secretKey"), sent as
	// the x-api-key credential to the standard endpoint.
	AccessToken string `json:"access_token"`
	// ZAIAccessToken is the Z.AI OAuth access token the key was provisioned from.
	// It is retained so the key can be re-provisioned if needed.
	ZAIAccessToken string `json:"zai_access_token,omitempty"`
	// BaseURL is the Anthropic-compatible endpoint that accepts the token.
	BaseURL string `json:"base_url,omitempty"`
	// UserID identifies the authenticated Z.AI / BigModel account.
	UserID string `json:"user_id,omitempty"`
	// Email is the authenticated account email, when provided.
	Email string `json:"email,omitempty"`
	// Name is the authenticated account display name, when provided.
	Name string `json:"name,omitempty"`
	// LastRefresh is the RFC3339 timestamp of the last credential update.
	LastRefresh string `json:"last_refresh,omitempty"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *TokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Z.AI token storage to a JSON file.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "zai"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("zai token storage: create directory: %w", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("zai token storage: create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	// Merge metadata using helper so hook-injected fields are flattened alongside
	// the declared struct fields.
	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("zai token storage: merge metadata: %w", errMerge)
	}

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("zai token storage: write token file: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used for Z.AI credentials, namespaced by
// the identity provider and a stable account identifier when available.
func CredentialFileName(provider, userID, email string) string {
	provider = sanitizeFileSegment(provider)
	if provider == "" {
		provider = "zai"
	}
	if seg := sanitizeFileSegment(email); seg != "" {
		return fmt.Sprintf("zai-%s-%s.json", provider, seg)
	}
	if seg := sanitizeFileSegment(userID); seg != "" {
		return fmt.Sprintf("zai-%s-%s.json", provider, seg)
	}
	return fmt.Sprintf("zai-%s-%d.json", provider, time.Now().UnixMilli())
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
