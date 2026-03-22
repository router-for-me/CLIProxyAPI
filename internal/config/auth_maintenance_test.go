package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_AuthMaintenancePreserves429StatusCode(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	data := []byte(`
auth-maintenance:
  enable: true
  delete-status-codes: [401, 429, 429, 700, 0]
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	want := []int{401, 429}
	if len(cfg.AuthMaintenance.DeleteStatusCodes) != len(want) {
		t.Fatalf("DeleteStatusCodes length = %d, want %d (%v)", len(cfg.AuthMaintenance.DeleteStatusCodes), len(want), cfg.AuthMaintenance.DeleteStatusCodes)
	}
	for i, code := range want {
		if cfg.AuthMaintenance.DeleteStatusCodes[i] != code {
			t.Fatalf("DeleteStatusCodes[%d] = %d, want %d (%v)", i, cfg.AuthMaintenance.DeleteStatusCodes[i], code, cfg.AuthMaintenance.DeleteStatusCodes)
		}
	}
}
