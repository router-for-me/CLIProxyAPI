package cliproxy

import (
	"os"
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func writeBaseConfigFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("failed to write base config file: %v", err)
	}
}

func TestPersistCircuitBreakerAutoRemoval_CodexAPIKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	svc := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey:   "k1",
				BaseURL:  "https://codex.example.com",
				Models:   []config.CodexModel{{Name: "gpt-5-codex", Alias: "alias-gpt-5-codex"}},
				Priority: 1,
			}},
		},
		configPath: configPath,
	}
	auth := &coreauth.Auth{
		ID:       "auth-codex-1",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "k1",
			"base_url":  "https://codex.example.com",
			"auth_kind": "apikey",
		},
	}

	persisted, alreadyRemoved, err := svc.persistCircuitBreakerAutoRemoval(auth, "alias-gpt-5-codex")
	if err != nil {
		t.Fatalf("persistCircuitBreakerAutoRemoval() error = %v", err)
	}
	if !persisted {
		t.Fatal("persisted = false, want true")
	}
	if alreadyRemoved {
		t.Fatal("alreadyRemoved = true, want false")
	}
	if len(svc.cfg.CodexKey) != 1 {
		t.Fatalf("codex entries len = %d, want 1", len(svc.cfg.CodexKey))
	}
	if len(svc.cfg.CodexKey[0].Models) != 0 {
		t.Fatalf("models len = %d, want 0", len(svc.cfg.CodexKey[0].Models))
	}
	if len(svc.cfg.CodexKey[0].ExcludedModels) != 1 || svc.cfg.CodexKey[0].ExcludedModels[0] != "alias-gpt-5-codex" {
		t.Fatalf("excluded models = %v, want [alias-gpt-5-codex]", svc.cfg.CodexKey[0].ExcludedModels)
	}

	persistedAgain, alreadyRemovedAgain, err := svc.persistCircuitBreakerAutoRemoval(auth, "alias-gpt-5-codex")
	if err != nil {
		t.Fatalf("second persistCircuitBreakerAutoRemoval() error = %v", err)
	}
	if persistedAgain {
		t.Fatal("second persisted = true, want false")
	}
	if !alreadyRemovedAgain {
		t.Fatal("second alreadyRemoved = false, want true")
	}
}

func TestPersistCircuitBreakerAutoRemoval_OAuthProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeBaseConfigFile(t, configPath)

	svc := &Service{
		cfg:        &config.Config{},
		configPath: configPath,
	}
	auth := &coreauth.Auth{
		ID:       "auth-qwen-oauth-1",
		Provider: "qwen",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	persisted, alreadyRemoved, err := svc.persistCircuitBreakerAutoRemoval(auth, "qwen-plus")
	if err != nil {
		t.Fatalf("persistCircuitBreakerAutoRemoval() error = %v", err)
	}
	if !persisted {
		t.Fatal("persisted = false, want true")
	}
	if alreadyRemoved {
		t.Fatal("alreadyRemoved = true, want false")
	}
	if got := svc.cfg.OAuthExcludedModels["qwen"]; len(got) != 1 || got[0] != "qwen-plus" {
		t.Fatalf("oauth-excluded-models[qwen] = %v, want [qwen-plus]", got)
	}
}

func TestNormalizeModelForAutoRemoval_StripsSuffixAndPrefix(t *testing.T) {
	auth := &coreauth.Auth{Prefix: "teamA"}
	got := normalizeModelForAutoRemoval(auth, "teamA/gpt-5-codex:high")
	if got != "gpt-5-codex" {
		t.Fatalf("normalizeModelForAutoRemoval() = %q, want %q", got, "gpt-5-codex")
	}
}
