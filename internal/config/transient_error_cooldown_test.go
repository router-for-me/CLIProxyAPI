package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigApplyRuntimeDefaults_TransientErrorCooldownDirectStruct(t *testing.T) {
	cfg := &Config{}

	cfg.ApplyRuntimeDefaults()

	if got := cfg.TransientErrorCooldownSeconds; got != DefaultTransientErrorCooldownSeconds {
		t.Fatalf("TransientErrorCooldownSeconds = %d, want %d", got, DefaultTransientErrorCooldownSeconds)
	}
}

func TestConfigSetTransientErrorCooldownSeconds_AllowsExplicitZero(t *testing.T) {
	cfg := &Config{}

	cfg.SetTransientErrorCooldownSeconds(0)
	cfg.ApplyRuntimeDefaults()

	if got := cfg.TransientErrorCooldownSeconds; got != 0 {
		t.Fatalf("TransientErrorCooldownSeconds = %d, want 0", got)
	}
}

func TestLoadConfigOptional_TransientErrorCooldownExplicitZero(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte("transient-error-cooldown-seconds: 0\n")
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.TransientErrorCooldownSeconds; got != 0 {
		t.Fatalf("TransientErrorCooldownSeconds = %d, want 0", got)
	}
}

func TestParseConfigBytes_TransientErrorCooldownExplicitZero(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("transient-error-cooldown-seconds: 0\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if got := cfg.TransientErrorCooldownSeconds; got != 0 {
		t.Fatalf("TransientErrorCooldownSeconds = %d, want 0", got)
	}
}
