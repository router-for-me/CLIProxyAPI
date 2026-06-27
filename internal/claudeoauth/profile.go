package claudeoauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const ProfileMetadataKey = "claude_oauth_profile"

var deviceIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Profile struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
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
		DeviceID:    deviceID,
		AccountUUID: strings.TrimSpace(accountUUID),
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

func Summary(profile Profile) map[string]any {
	return map[string]any{
		"device_id":    profile.DeviceID,
		"account_uuid": profile.AccountUUID,
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
