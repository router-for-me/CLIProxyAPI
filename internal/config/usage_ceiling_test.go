package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigRoutingUsageCeiling(t *testing.T) {
	data := []byte("config-version: 8\nrouting:\n  strategy: round-robin\n  usage-ceiling:\n    weekly-percent: 80\n    short-window-percent: 90\n")
	if errValidate := ValidateV8Config(data); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v", errValidate)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if got := cfg.Routing.UsageCeiling; got.WeeklyPercent != 80 || got.ShortWindowPercent != 90 {
		t.Fatalf("usage ceiling = %+v, want weekly 80 and short 90", got)
	}
}
