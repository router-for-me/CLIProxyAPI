package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestManagementForcedReloadReappliesUnchangedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("auth-dir: "+authDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	wrapper, err := defaultWatcherFactory(path, authDir, func(*config.Config) { reloads++ })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := wrapper.Stop(); err != nil {
			t.Errorf("stop watcher fixture: %v", err)
		}
	})
	service := &Service{}
	service.setWatcher(wrapper)

	service.reloadConfigAfterManagementSave(context.Background())
	service.reloadConfigAfterManagementSave(context.Background())
	if reloads != 1 {
		t.Fatalf("test setup: unchanged file reloads = %d, want 1", reloads)
	}
	// A plugin delete marks its reload forced: the save it races may already be applied.
	service.reloadConfigAfterManagementSave(management.WithForcedConfigReload(context.Background()))
	if reloads != 2 {
		t.Fatalf("forced management reload published %d configs, want 2", reloads)
	}
}
