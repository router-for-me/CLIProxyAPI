package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytesListUnprefixedModelsDefaultsToTrue(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models default = false, want true")
	}
	if !cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models default = false, want true")
	}
}

func TestParseConfigBytesListUnprefixedModelsCanBeDisabled(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("list-unprefixed-models: false\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models = true, want false")
	}
	if cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models = true, want false")
	}
}

func TestLoadConfigListUnprefixedModelsCanBeDisabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("list-unprefixed-models: false\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models = true, want false")
	}
	if cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models = true, want false")
	}
}
