package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_ClampsLogsMaxTotalSizeMB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("logs-max-total-size-mb: 1048577\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.LogsMaxTotalSizeMB != MaxLogsMaxTotalSizeMB {
		t.Fatalf("expected logs-max-total-size-mb to be clamped to %d, got %d", MaxLogsMaxTotalSizeMB, cfg.LogsMaxTotalSizeMB)
	}
}
