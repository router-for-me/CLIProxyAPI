package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ImportedCredential is the credential payload accepted by the Claude import
// login mode. The payload may be wrapped in a top-level "data" object, as in
// exports produced by Claude account tools.
type ImportedCredential struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        int64
	Scope            string
	LastRefresh      string
	Email            string
	AccountUUID      string
	OrganizationUUID string
	OrganizationName string
	SubscriptionType string
	OrganizationType string
	RateLimitTier    string
	metadata         map[string]any
}

// ParseImportedCredential parses either the documented {"data": {...}}
// envelope or the credential object itself. Unknown fields are retained as
// metadata so provider-specific account attributes survive the import.
func ParseImportedCredential(raw []byte) (*ImportedCredential, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse Claude credential JSON: %w", err)
	}
	if len(document) == 0 {
		return nil, fmt.Errorf("parse Claude credential JSON: object is empty")
	}
	if wrapped, ok := document["data"].(map[string]any); ok {
		document = wrapped
	}

	credential := &ImportedCredential{
		AccessToken:      stringValue(document["access_token"]),
		RefreshToken:     stringValue(document["refresh_token"]),
		Scope:            stringValue(document["scope"]),
		LastRefresh:      stringValue(document["last_refresh"]),
		Email:            stringValue(document["email"]),
		AccountUUID:      stringValue(document["account_uuid"]),
		OrganizationUUID: stringValue(document["organization_uuid"]),
		OrganizationName: stringValue(document["organization_name"]),
		SubscriptionType: stringValue(document["subscription_type"]),
		OrganizationType: stringValue(document["organization_type"]),
		RateLimitTier:    stringValue(document["rate_limit_tier"]),
		metadata:         cloneMetadata(document),
	}
	if rawExpiresAt := document["expires_at"]; rawExpiresAt != nil {
		expiresAt, err := unixTimestamp(rawExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse Claude credential expires_at: %w", err)
		}
		credential.ExpiresAt = expiresAt
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return nil, fmt.Errorf("Claude credential access_token is required")
	}
	if strings.TrimSpace(credential.Email) == "" {
		return nil, fmt.Errorf("Claude credential email is required")
	}
	return credential, nil
}

// BuildImportedAuth converts a parsed Claude credential into the canonical
// auth record consumed by the scheduler and Claude executor.
func BuildImportedAuth(credential *ImportedCredential) (*coreauth.Auth, error) {
	if credential == nil {
		return nil, fmt.Errorf("Claude credential is nil")
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return nil, fmt.Errorf("Claude credential access_token is required")
	}
	if strings.TrimSpace(credential.Email) == "" {
		return nil, fmt.Errorf("Claude credential email is required")
	}

	metadata := cloneMetadata(credential.metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["type"] = "claude"
	metadata["access_token"] = strings.TrimSpace(credential.AccessToken)
	metadata["refresh_token"] = strings.TrimSpace(credential.RefreshToken)
	metadata["email"] = strings.TrimSpace(credential.Email)
	if value := strings.TrimSpace(credential.Scope); value != "" {
		metadata["scope"] = value
		metadata["scopes"] = value
	}
	if value := strings.TrimSpace(credential.LastRefresh); value != "" {
		metadata["last_refresh"] = value
	}
	if credential.ExpiresAt > 0 {
		metadata["expires_at"] = credential.ExpiresAt
		metadata["expired"] = time.Unix(credential.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	for key, value := range map[string]string{
		"account_uuid":      credential.AccountUUID,
		"organization_uuid": credential.OrganizationUUID,
		"organization_name": credential.OrganizationName,
		"subscription_type": credential.SubscriptionType,
		"organization_type": credential.OrganizationType,
		"rate_limit_tier":   credential.RateLimitTier,
	} {
		if value = strings.TrimSpace(value); value != "" {
			metadata[key] = value
		}
	}

	expired := ""
	if credential.ExpiresAt > 0 {
		expired = time.Unix(credential.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	storage := &ClaudeTokenStorage{
		AccessToken:      strings.TrimSpace(credential.AccessToken),
		RefreshToken:     strings.TrimSpace(credential.RefreshToken),
		LastRefresh:      strings.TrimSpace(credential.LastRefresh),
		Email:            strings.TrimSpace(credential.Email),
		AccountUUID:      strings.TrimSpace(credential.AccountUUID),
		OrganizationUUID: strings.TrimSpace(credential.OrganizationUUID),
		OrganizationName: strings.TrimSpace(credential.OrganizationName),
		Expire:           expired,
		Metadata:         metadata,
	}
	fileName := CredentialFileName(storage.Email, storage.OrganizationUUID, storage.AccountUUID)
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "claude",
		FileName: fileName,
		Storage:  storage,
		Label:    storage.Email,
		Metadata: metadata,
	}, nil
}

func cloneMetadata(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func unixTimestamp(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	case float64:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("expected Unix timestamp, got %T", value)
	}
}
