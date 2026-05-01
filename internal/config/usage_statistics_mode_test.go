package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigOptional_UsageStatisticsMode(t *testing.T) {
	t.Run("explicit mode overrides legacy boolean", func(t *testing.T) {
		cfg := loadUsageStatisticsModeConfig(t, `
usage-statistics-enabled: true
usage-statistics:
  mode: "off"
`)

		if got := cfg.UsageStatistics.Mode; got != "off" {
			t.Fatalf("UsageStatistics.Mode = %q, want %q", got, "off")
		}
		if got := cfg.EffectiveUsageStatisticsMode(); got != "off" {
			t.Fatalf("EffectiveUsageStatisticsMode() = %q, want %q", got, "off")
		}
	})

	t.Run("legacy enabled uses memory mode", func(t *testing.T) {
		cfg := loadUsageStatisticsModeConfig(t, `
usage-statistics-enabled: true
`)

		if got := cfg.EffectiveUsageStatisticsMode(); got != "memory" {
			t.Fatalf("EffectiveUsageStatisticsMode() = %q, want %q", got, "memory")
		}
	})

	t.Run("legacy disabled uses off mode", func(t *testing.T) {
		cfg := loadUsageStatisticsModeConfig(t, `
usage-statistics-enabled: false
`)

		if got := cfg.EffectiveUsageStatisticsMode(); got != "off" {
			t.Fatalf("EffectiveUsageStatisticsMode() = %q, want %q", got, "off")
		}
	})

	t.Run("rejects unknown explicit mode", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		configYAML := []byte(`
usage-statistics:
  mode: "disk"
`)
		if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		_, err := LoadConfigOptional(configPath, false)
		if err == nil {
			t.Fatal("LoadConfigOptional() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "unknown usage-statistics mode") {
			t.Fatalf("LoadConfigOptional() error = %q, want unknown usage-statistics mode", err)
		}
	})
}

func loadUsageStatisticsModeConfig(t *testing.T, configYAML string) *Config {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	return cfg
}
