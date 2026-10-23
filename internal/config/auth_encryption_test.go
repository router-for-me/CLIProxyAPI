package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOAuthAuthEncryption(t *testing.T) {
	data := []byte("config-version: 8\noauth:\n  auth-dir: ./auths\n  auth-encryption:\n    enabled: true\n    key-file: /run/secrets/cliproxy_auth_key\n")
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
	if !cfg.AuthEncryption.Enabled || cfg.AuthEncryption.KeyFile != "/run/secrets/cliproxy_auth_key" {
		t.Fatalf("auth encryption = %+v, want enabled with key file", cfg.AuthEncryption)
	}
}

func TestLoadConfigAuthEncryptionDefaultsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\noauth:\n  auth-dir: ./auths\n"), 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if cfg.AuthEncryption.Enabled {
		t.Fatal("auth encryption enabled by default")
	}
}
