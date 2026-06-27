package claudeoauth

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestEnsureMetadataProfileDisabledSkips(t *testing.T) {
	metadata := map[string]any{"email": "user@example.com"}
	_, changed, err := EnsureMetadataProfile(metadata, &config.Config{}, "account-uuid")
	if err != nil {
		t.Fatalf("EnsureMetadataProfile() error = %v", err)
	}
	if changed {
		t.Fatal("disabled fingerprint should not change metadata")
	}
	if _, ok := metadata[ProfileMetadataKey]; ok {
		t.Fatal("disabled fingerprint should not add profile")
	}
}

func TestEnsureMetadataProfileGeneratesDeviceAndUsesAccountUUID(t *testing.T) {
	cfg := &config.Config{
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{Enabled: true},
	}
	metadata := map[string]any{"email": "user@example.com"}
	profile, changed, err := EnsureMetadataProfile(metadata, cfg, "account-uuid")
	if err != nil {
		t.Fatalf("EnsureMetadataProfile() error = %v", err)
	}
	if !changed {
		t.Fatal("expected metadata change")
	}
	if !ValidDeviceID(profile.DeviceID) {
		t.Fatalf("device_id = %q, want 64 lowercase hex", profile.DeviceID)
	}
	if profile.AccountUUID != "account-uuid" {
		t.Fatalf("account_uuid = %q, want account-uuid", profile.AccountUUID)
	}
	raw, errMarshal := json.Marshal(metadata[ProfileMetadataKey])
	if errMarshal != nil {
		t.Fatalf("marshal profile: %v", errMarshal)
	}
	if strings.Contains(string(raw), `"header"`) {
		t.Fatalf("generated profile should not include header: %s", raw)
	}
	if strings.Contains(string(raw), `"version"`) || strings.Contains(string(raw), `"created_at"`) {
		t.Fatalf("generated profile should only include device/account: %s", raw)
	}
}

func TestEnsureRawAuthProfileOnlyClaudeOAuth(t *testing.T) {
	cfg := &config.Config{
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{
			Enabled:                true,
			GenerateMissingProfile: true,
		},
	}
	raw := []byte(`{"type":"claude","access_token":"sk-ant-oat-test","refresh_token":"refresh","email":"user@example.com","account_uuid":"account-uuid"}`)
	out, changed, err := EnsureRawAuthProfile(raw, cfg)
	if err != nil {
		t.Fatalf("EnsureRawAuthProfile() error = %v", err)
	}
	if !changed {
		t.Fatal("expected Claude OAuth raw JSON to change")
	}
	var metadata map[string]any
	if err := json.Unmarshal(out, &metadata); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	profile, ok := ProfileFromMetadata(metadata)
	if !ok {
		t.Fatalf("missing %s in output: %s", ProfileMetadataKey, string(out))
	}
	if profile.AccountUUID != "account-uuid" {
		t.Fatalf("account_uuid = %q, want account-uuid", profile.AccountUUID)
	}

	apiKeyRaw := []byte(`{"type":"claude","api_key":"sk-ant-api03-test"}`)
	apiKeyOut, apiKeyChanged, err := EnsureRawAuthProfile(apiKeyRaw, cfg)
	if err != nil {
		t.Fatalf("EnsureRawAuthProfile(api key) error = %v", err)
	}
	if apiKeyChanged || strings.Contains(string(apiKeyOut), ProfileMetadataKey) {
		t.Fatalf("Claude API key file should not be modified: %s", string(apiKeyOut))
	}
}

func TestEnsureRawAuthProfileSkipsOldAuthWhenGenerationDisabled(t *testing.T) {
	cfg := &config.Config{
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{Enabled: true},
	}
	raw := []byte(`{"type":"claude","access_token":"sk-ant-oat-test","refresh_token":"refresh","email":"user@example.com","account_uuid":"account-uuid"}`)
	out, changed, err := EnsureRawAuthProfile(raw, cfg)
	if err != nil {
		t.Fatalf("EnsureRawAuthProfile() error = %v", err)
	}
	if changed {
		t.Fatal("old auth profile should not be generated without generate_missing_profile")
	}
	if string(out) != string(raw) {
		t.Fatalf("raw auth changed\nout=%s\nin=%s", out, raw)
	}
}
