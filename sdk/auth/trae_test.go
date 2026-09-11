package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestTraeAuthenticatorImportsCredentialReference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	root := t.TempDir()
	cliPath := filepath.Join(root, "traecli")
	cli := []byte("#!/bin/sh\nif [ \"$1\" = \"models\" ]; then\n  echo '[{\"name\":\"GPT-5.6-Sol\",\"config_name\":\"gpt-5.6-sol\",\"backend_model\":\"gpt-5.6-sol__dev\",\"provider\":\"trae\",\"context_window\":272000}]'\n  exit 0\nfi\nif [ \"$1\" = \"--version\" ]; then\n  echo 'traecli 0.204.1'\n  exit 0\nfi\nexit 1\n")
	if errWrite := os.WriteFile(cliPath, cli, 0o700); errWrite != nil {
		t.Fatal(errWrite)
	}
	authDir := filepath.Join(root, "cli")
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	authPath := filepath.Join(authDir, "auth.json")
	authJSON := map[string]any{
		"auth_mode": "trae",
		"trae": map[string]any{
			"access_token": "must-not-be-copied",
			"user_id":      "test-user",
			"region":       "CN",
		},
	}
	rawAuth, _ := json.Marshal(authJSON)
	if errWrite := os.WriteFile(authPath, rawAuth, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	t.Setenv("TRAECLI_PATH", cliPath)
	t.Setenv("TRAE_HOME", root)
	t.Setenv("TRAE_AUTH_PATH", authPath)
	t.Setenv("TRAE_MODELS_PATH", filepath.Join(authDir, "models_cache.json"))

	record, errLogin := (TraeAuthenticator{}).Login(context.Background(), &config.Config{}, nil)
	if errLogin != nil {
		t.Fatalf("Login() error = %v", errLogin)
	}
	if record.Provider != "trae" || record.Label != "test-user" {
		t.Fatalf("Login() record = %+v", record)
	}
	if _, copied := record.Metadata["access_token"]; copied {
		t.Fatal("Login() copied access_token into CLIProxyAPI metadata")
	}
	if record.Metadata["trae_auth_path"] != authPath || record.Metadata["client_version"] != "0.204.1" {
		t.Fatalf("Login() metadata = %+v", record.Metadata)
	}
}
