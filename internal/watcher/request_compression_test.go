package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestReloadConfigIfChangedRequestCompression(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig := func(data string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	var received *config.Config
	reloads := 0
	w := &Watcher{
		configPath: configPath,
		reloadCallback: func(cfg *config.Config) {
			reloads++
			received = cfg
		},
	}

	writeConfig("request-compression: auto\nrequest-compression-min-size: 32k\n")
	w.reloadConfigIfChanged()
	if reloads != 1 {
		t.Fatalf("reload count: got %d, want 1", reloads)
	}
	if received == nil || received.EffectiveRequestCompressionMode() != config.RequestCompressionAuto || received.EffectiveRequestCompressionMinBytes() != 32<<10 {
		t.Fatalf("unexpected reloaded compression config: %+v", received)
	}

	writeConfig("request-compression: gzip\n")
	w.reloadConfigIfChanged()
	if reloads != 1 {
		t.Fatalf("invalid config changed reload count: got %d, want 1", reloads)
	}
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if w.config == nil || w.config.EffectiveRequestCompressionMode() != config.RequestCompressionAuto {
		t.Fatalf("invalid reload replaced the active configuration: %+v", w.config)
	}
}
