package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestReloadConfigReappliesUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth-dir: "+authDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	w := &Watcher{configPath: configPath, authDir: authDir, reloadCallback: func(*config.Config) { reloads++ }}

	w.ReloadConfigIfChanged()
	w.ReloadConfigIfChanged()
	if reloads != 1 {
		t.Fatalf("test setup: unchanged file reloads = %d, want 1", reloads)
	}
	// Runtime state can drift from an unchanged file, e.g. after a plugin unload.
	w.ReloadConfig()
	if reloads != 2 {
		t.Fatalf("forced reload of an unchanged file published %d configs, want 2", reloads)
	}
}
