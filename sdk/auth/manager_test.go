package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
