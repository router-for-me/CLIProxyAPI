package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	return path
}

func TestLoadConfig_DefaultOAuthCallbackDisabled(t *testing.T) {
	path := writeTempConfigFile(t, "host: \"\"\nport: 8317\nauth-dir: \"~/.cli-proxy-api\"\n")
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OAuthCallback.Enabled {
		t.Fatal("expected oauth-callback.enabled default false")
	}
}

func TestLoadConfig_OAuthCallbackCustomValues(t *testing.T) {
	path := writeTempConfigFile(t, "host: \"\"\nport: 8317\nauth-dir: \"~/.cli-proxy-api\"\noauth-callback:\n  enabled: true\n  external-base-url: \"https://cliproxy.example.com\"\n  provider-paths:\n    codex: \"/oauth/callback/codex\"\n")
	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OAuthCallback.ExternalBaseURL != "https://cliproxy.example.com" {
		t.Fatalf("unexpected base url: %s", cfg.OAuthCallback.ExternalBaseURL)
	}
}
