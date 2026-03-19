package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsInvalidTrustedProxies(t *testing.T) {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("trusted-proxies:\n  - not-an-ip\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("expected trusted-proxies validation error")
	}
	if !strings.Contains(err.Error(), "trusted-proxies") {
		t.Fatalf("error = %v, want trusted-proxies context", err)
	}
}
