package claudeoauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	ProfileMetadataKey = "claude_oauth_profile"
	ProfileVersion     = 1

	defaultUserAgent      = "claude-cli/2.1.186 (external, cli)"
	defaultPackageVersion = "0.94.0"
	defaultRuntimeVersion = "v24.3.0"
	defaultOS             = "MacOS"
	defaultArch           = "arm64"
)

var deviceIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type HeaderProfile struct {
	UserAgent      string `json:"user_agent"`
	PackageVersion string `json:"package_version"`
	RuntimeVersion string `json:"runtime_version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
}

type Profile struct {
	Version     int           `json:"version"`
	CreatedAt   string        `json:"created_at"`
	DeviceID    string        `json:"device_id"`
	AccountUUID string        `json:"account_uuid"`
	Header      HeaderProfile `json:"header"`
}

func Enabled(cfg *config.Config) bool {
	return cfg != nil && cfg.ClaudeOAuthFingerprint.Enabled
}

func OverrideDevice(cfg *config.Config) bool {
	return Enabled(cfg) && cfg.ClaudeOAuthFingerprint.OverrideDevice
}

func GenerateMissingProfile(cfg *config.Config) bool {
	return Enabled(cfg) && cfg.ClaudeOAuthFingerprint.GenerateMissingProfile
}

func IsClaudeOAuthAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return false
	}
	return auth.AuthKind() == cliproxyauth.AuthKindOAuth
}

func EnsureAuthProfile(auth *cliproxyauth.Auth, cfg *config.Config) (bool, error) {
	if !Enabled(cfg) || !IsClaudeOAuthAuth(auth) {
		return false, nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	if _, ok := ProfileFromMetadata(auth.Metadata); !ok && !GenerateMissingProfile(cfg) {
		return false, nil
	}
	_, changed, err := EnsureMetadataProfile(auth.Metadata, cfg, MetadataString(auth.Metadata, "account_uuid"))
	return changed, err
}

func AuthMetadata(cfg *config.Config, email, accountUUID string) (map[string]any, error) {
	metadata := map[string]any{
		"email": strings.TrimSpace(email),
	}
	accountUUID = strings.TrimSpace(accountUUID)
	if accountUUID != "" {
		metadata["account_uuid"] = accountUUID
	}
	if _, _, err := EnsureMetadataProfile(metadata, cfg, accountUUID); err != nil {
		return nil, err
	}
	return metadata, nil
}

func EnsureMetadataProfile(metadata map[string]any, cfg *config.Config, accountUUID string) (Profile, bool, error) {
	if !Enabled(cfg) {
		return Profile{}, false, nil
	}
	if metadata == nil {
		return Profile{}, false, fmt.Errorf("claude oauth profile: metadata is nil")
	}
	current, _ := ProfileFromMetadata(metadata)
	profile, changed, err := NormalizeProfile(current, cfg, accountUUID)
	if err != nil {
		return Profile{}, false, err
	}
	if !changed {
		return profile, false, nil
	}
	metadata[ProfileMetadataKey] = profile
	return profile, true, nil
}

func EnsureRawAuthProfile(data []byte, cfg *config.Config) ([]byte, bool, error) {
	if !Enabled(cfg) {
		return data, false, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return data, false, err
	}
	if !isClaudeOAuthMetadata(metadata) {
		return data, false, nil
	}
	if _, ok := ProfileFromMetadata(metadata); !ok && !GenerateMissingProfile(cfg) {
		return data, false, nil
	}
	_, changed, err := EnsureMetadataProfile(metadata, cfg, MetadataString(metadata, "account_uuid"))
	if err != nil || !changed {
		return data, changed, err
	}
	out, errMarshal := json.MarshalIndent(metadata, "", "  ")
	if errMarshal != nil {
		return data, false, errMarshal
	}
	out = append(out, '\n')
	return out, true, nil
}

func ProfileFromAuth(auth *cliproxyauth.Auth) (Profile, bool) {
	if auth == nil {
		return Profile{}, false
	}
	return ProfileFromMetadata(auth.Metadata)
}

func ProfileFromMetadata(metadata map[string]any) (Profile, bool) {
	if metadata == nil {
		return Profile{}, false
	}
	raw, ok := metadata[ProfileMetadataKey]
	if !ok || raw == nil {
		return Profile{}, false
	}
	var profile Profile
	switch typed := raw.(type) {
	case Profile:
		profile = typed
	case map[string]any:
		data, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return Profile{}, false
		}
		if errUnmarshal := json.Unmarshal(data, &profile); errUnmarshal != nil {
			return Profile{}, false
		}
	default:
		return Profile{}, false
	}
	if strings.TrimSpace(profile.DeviceID) == "" {
		return Profile{}, false
	}
	return profile, true
}

func NormalizeProfile(profile Profile, cfg *config.Config, accountUUID string) (Profile, bool, error) {
	original, _ := json.Marshal(profile)
	changed := false
	if profile.Version == 0 {
		profile.Version = ProfileVersion
		changed = true
	}
	if strings.TrimSpace(profile.CreatedAt) == "" {
		profile.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		changed = true
	}
	if !ValidDeviceID(profile.DeviceID) {
		deviceID, err := GenerateDeviceID()
		if err != nil {
			return Profile{}, false, err
		}
		profile.DeviceID = deviceID
		changed = true
	}
	if strings.TrimSpace(profile.AccountUUID) == "" && strings.TrimSpace(accountUUID) != "" {
		profile.AccountUUID = strings.TrimSpace(accountUUID)
		changed = true
	}
	header := normalizeHeaderProfile(profile.Header, DefaultHeaderProfile(cfg))
	if header != profile.Header {
		profile.Header = header
		changed = true
	}
	normalized, _ := json.Marshal(profile)
	if string(original) != string(normalized) {
		changed = true
	}
	return profile, changed, nil
}

func GenerateProfile(cfg *config.Config, accountUUID string) (Profile, error) {
	deviceID, err := GenerateDeviceID()
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		Version:     ProfileVersion,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		DeviceID:    deviceID,
		AccountUUID: strings.TrimSpace(accountUUID),
		Header:      DefaultHeaderProfile(cfg),
	}, nil
}

func GenerateDeviceID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("claude oauth profile: generate device id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func ValidDeviceID(deviceID string) bool {
	return deviceIDPattern.MatchString(strings.TrimSpace(deviceID))
}

func DefaultHeaderProfile(cfg *config.Config) HeaderProfile {
	hdrDefault := func(cfgVal, fallback string) string {
		if strings.TrimSpace(cfgVal) != "" {
			return strings.TrimSpace(cfgVal)
		}
		return fallback
	}
	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}
	return HeaderProfile{
		UserAgent:      hdrDefault(hd.UserAgent, defaultUserAgent),
		PackageVersion: hdrDefault(hd.PackageVersion, defaultPackageVersion),
		RuntimeVersion: hdrDefault(hd.RuntimeVersion, defaultRuntimeVersion),
		OS:             hdrDefault(hd.OS, defaultOS),
		Arch:           hdrDefault(hd.Arch, defaultArch),
	}
}

func Summary(profile Profile) map[string]any {
	return map[string]any{
		"version":      profile.Version,
		"created_at":   profile.CreatedAt,
		"device_id":    profile.DeviceID,
		"account_uuid": profile.AccountUUID,
		"header":       profile.Header,
	}
}

func MetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func normalizeHeaderProfile(header, fallback HeaderProfile) HeaderProfile {
	if strings.TrimSpace(header.UserAgent) == "" {
		header.UserAgent = fallback.UserAgent
	}
	if strings.TrimSpace(header.PackageVersion) == "" {
		header.PackageVersion = fallback.PackageVersion
	}
	if strings.TrimSpace(header.RuntimeVersion) == "" {
		header.RuntimeVersion = fallback.RuntimeVersion
	}
	if strings.TrimSpace(header.OS) == "" {
		header.OS = fallback.OS
	}
	if strings.TrimSpace(header.Arch) == "" {
		header.Arch = fallback.Arch
	}
	return header
}

func isClaudeOAuthMetadata(metadata map[string]any) bool {
	provider := strings.ToLower(strings.TrimSpace(MetadataString(metadata, "type")))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(MetadataString(metadata, "provider")))
	}
	if provider != "claude" {
		return false
	}
	if strings.EqualFold(MetadataString(metadata, "auth_kind"), cliproxyauth.AuthKindAPIKey) {
		return false
	}
	return MetadataString(metadata, "access_token") != "" ||
		MetadataString(metadata, "refresh_token") != "" ||
		strings.EqualFold(MetadataString(metadata, "auth_kind"), cliproxyauth.AuthKindOAuth)
}
