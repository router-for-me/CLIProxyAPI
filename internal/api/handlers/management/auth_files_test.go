package management

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudeoauth"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeOAuthMetadataAddsProfile(t *testing.T) {
	cfg := &config.Config{
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{Enabled: true},
	}
	metadata, err := claudeoauth.AuthMetadata(cfg, "user@example.com", "account-uuid")
	if err != nil {
		t.Fatalf("AuthMetadata() error = %v", err)
	}
	profile, ok := claudeoauth.ProfileFromMetadata(metadata)
	if !ok {
		t.Fatalf("missing %s", claudeoauth.ProfileMetadataKey)
	}
	if profile.AccountUUID != "account-uuid" {
		t.Fatalf("account_uuid = %q, want account-uuid", profile.AccountUUID)
	}
}

func TestWriteAuthFileInjectsClaudeOAuthProfile(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{cfg: &config.Config{
		AuthDir: dir,
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{
			Enabled:                true,
			GenerateMissingProfile: true,
		},
	}}
	raw := []byte(`{"type":"claude","access_token":"sk-ant-oat-test","refresh_token":"refresh","email":"user@example.com","account_uuid":"account-uuid"}`)
	if err := h.writeAuthFile(context.Background(), "claude-user.json", raw); err != nil {
		t.Fatalf("writeAuthFile() error = %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "claude-user.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(saved, &metadata); err != nil {
		t.Fatalf("unmarshal saved auth: %v", err)
	}
	profile, ok := claudeoauth.ProfileFromMetadata(metadata)
	if !ok {
		t.Fatalf("missing %s in saved auth: %s", claudeoauth.ProfileMetadataKey, string(saved))
	}
	if !claudeoauth.ValidDeviceID(profile.DeviceID) {
		t.Fatalf("device_id = %q, want 64 lowercase hex", profile.DeviceID)
	}
}

func TestBuildAuthFileEntryExposesClaudeOAuthProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-user.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	profile := claudeoauth.Profile{
		Version:     claudeoauth.ProfileVersion,
		CreatedAt:   "2026-06-26T00:00:00Z",
		DeviceID:    strings.Repeat("a", 64),
		AccountUUID: "account-uuid",
		Header:      claudeoauth.DefaultHeaderProfile(&config.Config{}),
	}
	h := &Handler{}
	entry := h.buildAuthFileEntry(&coreauth.Auth{
		ID:       "claude-user.json",
		Provider: "claude",
		FileName: "claude-user.json",
		Attributes: map[string]string{
			"path": path,
		},
		Metadata: map[string]any{
			"access_token":                 "sk-ant-oat-test",
			claudeoauth.ProfileMetadataKey: profile,
		},
	})
	if entry == nil {
		t.Fatal("buildAuthFileEntry() returned nil")
	}
	rawSummary, ok := entry[claudeoauth.ProfileMetadataKey].(map[string]any)
	if !ok {
		t.Fatalf("entry missing %s: %#v", claudeoauth.ProfileMetadataKey, entry)
	}
	if rawSummary["device_id"] != profile.DeviceID {
		t.Fatalf("device_id = %v, want %s", rawSummary["device_id"], profile.DeviceID)
	}
}
