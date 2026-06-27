package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudeoauth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestClaudeAuthMetadataAddsOAuthProfile(t *testing.T) {
	cfg := &config.Config{
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{Enabled: true},
	}
	metadata, err := claudeoauth.AuthMetadata(cfg, "user@example.com", "account-uuid")
	if err != nil {
		t.Fatalf("AuthMetadata() error = %v", err)
	}
	if metadata["account_uuid"] != "account-uuid" {
		t.Fatalf("account_uuid = %v, want account-uuid", metadata["account_uuid"])
	}
	profile, ok := claudeoauth.ProfileFromMetadata(metadata)
	if !ok {
		t.Fatalf("missing %s", claudeoauth.ProfileMetadataKey)
	}
	if !claudeoauth.ValidDeviceID(profile.DeviceID) {
		t.Fatalf("device_id = %q, want 64 lowercase hex", profile.DeviceID)
	}
	if profile.AccountUUID != "account-uuid" {
		t.Fatalf("profile account_uuid = %q, want account-uuid", profile.AccountUUID)
	}
}
