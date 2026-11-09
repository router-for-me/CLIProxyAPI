package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// TokenStorage is the persisted Qoder OAuth credential. ExpireTime uses Unix seconds,
// matching the official CLI credential shape; Expired also serves the shared scheduler.
type TokenStorage struct {
	Type                   string         `json:"type"`
	Region                 string         `json:"region"`
	AccessToken            string         `json:"access_token"`
	SecurityOAuthToken     string         `json:"security_oauth_token,omitempty"`
	RefreshToken           string         `json:"refresh_token"`
	ExpireTime             int64          `json:"expire_time,omitempty"`
	Expired                string         `json:"expired,omitempty"`
	RefreshTokenExpireTime int64          `json:"refresh_token_expire_time,omitempty"`
	RefreshTokenExpired    string         `json:"refresh_token_expired,omitempty"`
	UserID                 string         `json:"user_id"`
	Name                   string         `json:"name,omitempty"`
	Email                  string         `json:"email,omitempty"`
	MachineID              string         `json:"machine_id"`
	MachineToken           string         `json:"machine_token,omitempty"`
	MachineType            string         `json:"machine_type,omitempty"`
	MachineCode            string         `json:"machine_code,omitempty"`
	OrganizationID         string         `json:"organization_id,omitempty"`
	OrganizationName       string         `json:"organization_name,omitempty"`
	OrganizationTags       []string       `json:"organization_tags,omitempty"`
	DataPolicyAgreed       bool           `json:"data_policy_agreed,omitempty"`
	LastRefresh            string         `json:"last_refresh,omitempty"`
	RefreshIntervalSeconds int64          `json:"refresh_interval_seconds,omitempty"`
	Metadata               map[string]any `json:"-"`
}

// SetMetadata injects shared auth metadata before persistence.
func (s *TokenStorage) SetMetadata(metadata map[string]any) {
	s.Metadata = metadata
}

// SaveTokenToFile persists the credential with metadata merged at the top level.
func (s *TokenStorage) SaveTokenToFile(path string) error {
	if s == nil {
		return fmt.Errorf("qoder: token storage is nil")
	}
	s.Type = ProviderForRegion(s.Region)
	s.Region = NormalizeRegion(s.Region)
	if s.AccessToken == "" {
		s.AccessToken = s.SecurityOAuthToken
	}
	if s.SecurityOAuthToken == "" {
		s.SecurityOAuthToken = s.AccessToken
	}
	misc.LogSavingCredentials(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("qoder: create credential directory: %w", err)
	}
	data, err := misc.MergeMetadata(s, s.Metadata)
	if err != nil {
		return fmt.Errorf("qoder: merge credential metadata: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("qoder: create credential file: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Errorf("qoder: close credential file: %v", errClose)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if errEncode := encoder.Encode(data); errEncode != nil {
		return fmt.Errorf("qoder: write credential file: %w", errEncode)
	}
	return nil
}

// Expiry returns the access-token expiry when one was supplied.
func (s *TokenStorage) Expiry() (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	if s.ExpireTime > 0 {
		return time.Unix(s.ExpireTime, 0), true
	}
	if parsed, ok := parseExpiry(s.Expired, 0, time.Time{}); ok {
		return parsed, true
	}
	return time.Time{}, false
}

// CredentialFromAuth accepts both freshly-created Storage and file-watcher Metadata.
func CredentialFromAuth(auth *coreauth.Auth) (*TokenStorage, error) {
	if auth == nil {
		return nil, fmt.Errorf("qoder: credential is missing")
	}
	var storage *TokenStorage
	if typed, ok := auth.Storage.(*TokenStorage); ok && typed != nil {
		copy := *typed
		storage = &copy
	}
	if len(auth.Metadata) > 0 && firstMetadataString(auth.Metadata, "access_token", "token", "security_oauth_token") != "" {
		fromMetadata, err := CredentialFromMetadata(auth.Metadata)
		if err != nil {
			return nil, err
		}
		storage = fromMetadata
	}
	if storage == nil {
		return nil, fmt.Errorf("qoder: credential data is missing")
	}
	storage.Region = RegionFromAuth(auth.Provider, auth.Metadata)
	if storage.Region == "" {
		storage.Region = RegionGlobal
	}
	if storage.AccessToken == "" {
		storage.AccessToken = storage.SecurityOAuthToken
	}
	if storage.SecurityOAuthToken == "" {
		storage.SecurityOAuthToken = storage.AccessToken
	}
	if strings.TrimSpace(storage.AccessToken) == "" {
		return nil, fmt.Errorf("qoder: access token is missing")
	}
	if strings.TrimSpace(storage.MachineID) == "" {
		return nil, fmt.Errorf("qoder: machine id is missing")
	}
	return storage, nil
}

// CredentialFromMetadata decodes current and official CLI field aliases.
func CredentialFromMetadata(metadata map[string]any) (*TokenStorage, error) {
	if metadata == nil {
		return nil, fmt.Errorf("qoder: credential metadata is missing")
	}
	storage := &TokenStorage{
		Type:                   String(metadata, "type"),
		Region:                 NormalizeRegion(String(metadata, "region")),
		AccessToken:            firstMetadataString(metadata, "access_token", "token", "security_oauth_token"),
		SecurityOAuthToken:     firstMetadataString(metadata, "security_oauth_token", "access_token", "token"),
		RefreshToken:           String(metadata, "refresh_token"),
		UserID:                 firstMetadataString(metadata, "user_id", "uid"),
		Name:                   String(metadata, "name"),
		Email:                  String(metadata, "email"),
		MachineID:              firstMetadataString(metadata, "machine_id", "machineId"),
		MachineToken:           firstMetadataString(metadata, "machine_token", "machineToken"),
		MachineType:            firstMetadataString(metadata, "machine_type", "machineType"),
		MachineCode:            firstMetadataString(metadata, "machine_code", "machineCode"),
		OrganizationID:         firstMetadataString(metadata, "organization_id", "organizationId", "orgId"),
		OrganizationName:       firstMetadataString(metadata, "organization_name", "organizationName", "orgName"),
		DataPolicyAgreed:       boolValue(metadata["data_policy_agreed"]),
		LastRefresh:            String(metadata, "last_refresh"),
		RefreshIntervalSeconds: int64Value(metadata["refresh_interval_seconds"]),
	}
	storage.OrganizationTags = stringSlice(metadata["organization_tags"])
	storage.ExpireTime = expirySeconds(metadata, "expire_time", "expires_at", "expired")
	storage.RefreshTokenExpireTime = expirySeconds(metadata, "refresh_token_expire_time", "refresh_token_expires_at", "refresh_token_expired")
	if storage.ExpireTime > 0 {
		storage.Expired = time.Unix(storage.ExpireTime, 0).UTC().Format(time.RFC3339)
	} else {
		storage.Expired = String(metadata, "expired")
	}
	if storage.RefreshTokenExpireTime > 0 {
		storage.RefreshTokenExpired = time.Unix(storage.RefreshTokenExpireTime, 0).UTC().Format(time.RFC3339)
	}
	return storage, nil
}

// ApplyMetadata updates all token lifecycle fields while preserving user-managed metadata.
func (s *TokenStorage) ApplyMetadata(metadata map[string]any) {
	if s == nil || metadata == nil {
		return
	}
	metadata["type"] = ProviderForRegion(s.Region)
	metadata["region"] = NormalizeRegion(s.Region)
	metadata["access_token"] = s.AccessToken
	metadata["security_oauth_token"] = s.AccessToken
	metadata["refresh_token"] = s.RefreshToken
	metadata["user_id"] = s.UserID
	metadata["name"] = s.Name
	metadata["email"] = s.Email
	metadata["machine_id"] = s.MachineID
	metadata["organization_id"] = s.OrganizationID
	metadata["organization_name"] = s.OrganizationName
	metadata["organization_tags"] = append([]string(nil), s.OrganizationTags...)
	metadata["data_policy_agreed"] = s.DataPolicyAgreed
	metadata["last_refresh"] = s.LastRefresh
	metadata["refresh_interval_seconds"] = s.RefreshIntervalSeconds
	setOrDeleteString(metadata, "machine_token", s.MachineToken)
	setOrDeleteString(metadata, "machine_type", s.MachineType)
	setOrDeleteString(metadata, "machine_code", s.MachineCode)
	if s.ExpireTime > 0 {
		metadata["expire_time"] = s.ExpireTime
		metadata["expired"] = time.Unix(s.ExpireTime, 0).UTC().Format(time.RFC3339)
	} else {
		delete(metadata, "expire_time")
		delete(metadata, "expired")
	}
	if s.RefreshTokenExpireTime > 0 {
		metadata["refresh_token_expire_time"] = s.RefreshTokenExpireTime
		metadata["refresh_token_expired"] = time.Unix(s.RefreshTokenExpireTime, 0).UTC().Format(time.RFC3339)
	} else {
		delete(metadata, "refresh_token_expire_time")
		delete(metadata, "refresh_token_expired")
	}
}

// String returns a trimmed string metadata value.
func String(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func firstMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := String(metadata, key); value != "" {
			return value
		}
	}
	return ""
}

func setOrDeleteString(metadata map[string]any, key, value string) {
	if strings.TrimSpace(value) == "" {
		delete(metadata, key)
		return
	}
	metadata[key] = value
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func expirySeconds(metadata map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, exists := metadata[key]
		if !exists || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(text)); err == nil {
				return parsed.Unix()
			}
		}
		numeric := int64Value(value)
		if numeric > 10_000_000_000 {
			numeric /= 1000
		}
		if numeric > 0 {
			return numeric
		}
	}
	return 0
}
