package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type dummyAuthenticator struct {
	provider string
	record   *coreauth.Auth
}

func (d *dummyAuthenticator) Provider() string {
	return d.provider
}

func (d *dummyAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	return d.record, nil
}

func (d *dummyAuthenticator) RefreshLead() *time.Duration {
	return nil
}

func TestManagerLogin_PreservesExistingAuthFileMetadata(t *testing.T) {
	authDir := t.TempDir()
	fileName := "demo.json"
	filePath := filepath.Join(authDir, fileName)

	// Pre-populate existing auth file with custom settings
	existing := map[string]any{
		"type":         "demo",
		"email":        "user@example.com",
		"access_token": "old-token",
		"prefix":       "my-prefix",
		"websockets":   false,
		"note":         "important note",
		"weight":       float64(10),
	}
	raw, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatalf("marshal error: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filePath, raw, 0o600); errWrite != nil {
		t.Fatalf("write error: %v", errWrite)
	}

	newRecord := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "demo",
		Metadata: map[string]any{
			"type":         "demo",
			"email":        "user@example.com",
			"access_token": "new-token",
		},
	}

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	auth := &dummyAuthenticator{
		provider: "demo",
		record:   newRecord,
	}

	mgr := NewManager(store, auth)
	cfg := &config.Config{
		AuthDir: authDir,
	}

	_, savedPath, errLogin := mgr.Login(context.Background(), "demo", cfg, nil)
	if errLogin != nil {
		t.Fatalf("Login error: %v", errLogin)
	}
	if savedPath != filePath {
		t.Fatalf("savedPath = %s, want %s", savedPath, filePath)
	}

	savedRaw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("ReadFile error: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal error: %v", errUnmarshal)
	}

	if saved["access_token"] != "new-token" {
		t.Errorf("access_token = %v, want new-token", saved["access_token"])
	}
	if saved["prefix"] != "my-prefix" {
		t.Errorf("prefix = %v, want my-prefix", saved["prefix"])
	}
	if saved["websockets"] != false {
		t.Errorf("websockets = %v, want false", saved["websockets"])
	}
	if saved["note"] != "important note" {
		t.Errorf("note = %v, want important note", saved["note"])
	}
	if saved["weight"] != float64(10) {
		t.Errorf("weight = %v, want 10", saved["weight"])
	}
}

func TestManagerLogin_MigratesMatchingLegacyClaudeCredential(t *testing.T) {
	authDir := t.TempDir()
	legacyFileName := "claude-user@example.com.json"
	targetFileName := claudeauth.CredentialFileName("user@example.com", "organization-a", "account-a")
	legacyPath := filepath.Join(authDir, legacyFileName)
	targetPath := filepath.Join(authDir, targetFileName)

	existing := map[string]any{
		"type":              "claude",
		"email":             "user@example.com",
		"organization_uuid": "organization-a",
		"account_uuid":      "account-a",
		"access_token":      "old-token",
		"prefix":            "team",
		"proxy_url":         "http://127.0.0.1:8080",
		"disabled":          true,
		"weight":            float64(5),
	}
	raw, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatalf("marshal legacy credential: %v", errMarshal)
	}
	if errWrite := os.WriteFile(legacyPath, raw, 0o600); errWrite != nil {
		t.Fatalf("write legacy credential: %v", errWrite)
	}

	newRecord := &coreauth.Auth{
		ID:       targetFileName,
		FileName: targetFileName,
		Provider: "claude",
		Storage: &claudeauth.ClaudeTokenStorage{
			Type:             "claude",
			Email:            "user@example.com",
			OrganizationUUID: "organization-a",
			AccountUUID:      "account-a",
			AccessToken:      "new-token",
		},
		Metadata: map[string]any{
			"type":              "claude",
			"email":             "user@example.com",
			"organization_uuid": "organization-a",
			"account_uuid":      "account-a",
			"access_token":      "new-token",
		},
	}

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)
	mgr := NewManager(store, &dummyAuthenticator{provider: "claude", record: newRecord})

	_, savedPath, errLogin := mgr.Login(context.Background(), "claude", &config.Config{AuthDir: authDir}, nil)
	if errLogin != nil {
		t.Fatalf("Login error: %v", errLogin)
	}
	if savedPath != targetPath {
		t.Fatalf("savedPath = %s, want %s", savedPath, targetPath)
	}
	if _, errStat := os.Stat(legacyPath); !os.IsNotExist(errStat) {
		t.Fatalf("legacy credential still exists or stat failed: %v", errStat)
	}

	savedRaw, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read migrated credential: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal migrated credential: %v", errUnmarshal)
	}
	for key, want := range map[string]any{
		"access_token": "new-token",
		"prefix":       "team",
		"proxy_url":    "http://127.0.0.1:8080",
		"disabled":     true,
		"weight":       float64(5),
	} {
		if got := saved[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestManagerLogin_DoesNotMigrateLegacyClaudeCredentialFromDifferentOrganization(t *testing.T) {
	authDir := t.TempDir()
	legacyFileName := "claude-user@example.com.json"
	targetFileName := claudeauth.CredentialFileName("user@example.com", "organization-b", "shared-account")
	legacyPath := filepath.Join(authDir, legacyFileName)

	existing := map[string]any{
		"type":              "claude",
		"email":             "user@example.com",
		"organization_uuid": "organization-a",
		"account_uuid":      "shared-account",
		"access_token":      "old-token",
		"prefix":            "team",
	}
	raw, errMarshal := json.Marshal(existing)
	if errMarshal != nil {
		t.Fatalf("marshal legacy credential: %v", errMarshal)
	}
	if errWrite := os.WriteFile(legacyPath, raw, 0o600); errWrite != nil {
		t.Fatalf("write legacy credential: %v", errWrite)
	}

	newRecord := &coreauth.Auth{
		ID:       targetFileName,
		FileName: targetFileName,
		Provider: "claude",
		Metadata: map[string]any{
			"type":              "claude",
			"email":             "user@example.com",
			"organization_uuid": "organization-b",
			"account_uuid":      "shared-account",
			"access_token":      "new-token",
		},
	}

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)
	mgr := NewManager(store, &dummyAuthenticator{provider: "claude", record: newRecord})

	if _, _, errLogin := mgr.Login(context.Background(), "claude", &config.Config{AuthDir: authDir}, nil); errLogin != nil {
		t.Fatalf("Login error: %v", errLogin)
	}
	if _, errStat := os.Stat(legacyPath); errStat != nil {
		t.Fatalf("different-organization legacy credential was removed: %v", errStat)
	}

	savedRaw, errRead := os.ReadFile(filepath.Join(authDir, targetFileName))
	if errRead != nil {
		t.Fatalf("read new credential: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal new credential: %v", errUnmarshal)
	}
	if _, inherited := saved["prefix"]; inherited {
		t.Fatalf("new credential inherited prefix from a different organization: %#v", saved["prefix"])
	}
}
