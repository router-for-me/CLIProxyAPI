package mongostate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRuntimeConfig_MissingFileReturnsDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	cfg, path, found, err := LoadRuntimeConfig(configPath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if found {
		t.Fatal("LoadRuntimeConfig() found = true, want false")
	}
	if filepath.Base(path) != "state-store.local.ini" {
		t.Fatalf("LoadRuntimeConfig() path = %q, want state-store.local.ini", path)
	}
	if cfg.Enabled {
		t.Fatal("cfg.Enabled = true, want false")
	}
	if cfg.Database != "cliproxy_state" {
		t.Fatalf("cfg.Database = %q, want cliproxy_state", cfg.Database)
	}
	if cfg.FlushIntervalSeconds != 30 {
		t.Fatalf("cfg.FlushIntervalSeconds = %d, want 30", cfg.FlushIntervalSeconds)
	}
}

func TestLoadRuntimeConfig_ParsesINIAndNormalizesDefaults(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	statePath := filepath.Join(tempDir, "state-store.local.ini")
	content := []byte("[mongo]\nenabled = true\nuri = mongodb://127.0.0.1:27017\ndatabase = custom_state\nsnapshot_collection = custom_snapshots\nconnect_timeout_seconds = 21\noperation_timeout_seconds = 8\nflush_interval_seconds = 99\n")
	if err := os.WriteFile(statePath, content, 0o600); err != nil {
		t.Fatalf("write state-store.local.ini: %v", err)
	}

	cfg, path, found, err := LoadRuntimeConfig(configPath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if !found {
		t.Fatal("LoadRuntimeConfig() found = false, want true")
	}
	if path != statePath {
		t.Fatalf("LoadRuntimeConfig() path = %q, want %q", path, statePath)
	}
	if !cfg.Enabled {
		t.Fatal("cfg.Enabled = false, want true")
	}
	if cfg.URI != "mongodb://127.0.0.1:27017" {
		t.Fatalf("cfg.URI = %q, want explicit value", cfg.URI)
	}
	if cfg.Database != "custom_state" {
		t.Fatalf("cfg.Database = %q, want custom_state", cfg.Database)
	}
	if cfg.SnapshotCollection != "custom_snapshots" {
		t.Fatalf("cfg.SnapshotCollection = %q, want custom_snapshots", cfg.SnapshotCollection)
	}
	if cfg.ConnectTimeoutSeconds != 21 {
		t.Fatalf("cfg.ConnectTimeoutSeconds = %d, want 21", cfg.ConnectTimeoutSeconds)
	}
	if cfg.OperationTimeoutSeconds != 8 {
		t.Fatalf("cfg.OperationTimeoutSeconds = %d, want 8", cfg.OperationTimeoutSeconds)
	}
	if cfg.FlushIntervalSeconds != 99 {
		t.Fatalf("cfg.FlushIntervalSeconds = %d, want 99", cfg.FlushIntervalSeconds)
	}
}

func TestLoadRuntimeConfig_EmptyFileFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	statePath := filepath.Join(tempDir, "state-store.local.ini")
	if err := os.WriteFile(statePath, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("write state-store.local.ini: %v", err)
	}

	_, _, found, err := LoadRuntimeConfig(configPath)
	if err == nil {
		t.Fatal("LoadRuntimeConfig() error = nil, want error")
	}
	if !found {
		t.Fatal("LoadRuntimeConfig() found = false, want true")
	}
}

func TestApplyEnvOverrides_OverridesRuntimeConfig(t *testing.T) {
	t.Setenv("MONGOSTATE_ENABLED", "true")
	t.Setenv("MONGOSTATE_URI", " mongodb://env-host:27017 ")
	t.Setenv("MONGOSTATE_DATABASE", "env_state")
	t.Setenv("MONGOSTATE_SNAPSHOT_COLLECTION", "env_snapshots")
	t.Setenv("MONGOSTATE_CONNECT_TIMEOUT_SECONDS", "17")
	t.Setenv("MONGOSTATE_OPERATION_TIMEOUT_SECONDS", "11")
	t.Setenv("MONGOSTATE_FLUSH_INTERVAL_SECONDS", "41")

	cfg := DefaultRuntimeConfig()
	ApplyEnvOverrides(&cfg)

	if !cfg.Enabled {
		t.Fatal("cfg.Enabled = false, want true")
	}
	if cfg.URI != "mongodb://env-host:27017" {
		t.Fatalf("cfg.URI = %q, want trimmed env value", cfg.URI)
	}
	if cfg.Database != "env_state" {
		t.Fatalf("cfg.Database = %q, want env_state", cfg.Database)
	}
	if cfg.SnapshotCollection != "env_snapshots" {
		t.Fatalf("cfg.SnapshotCollection = %q, want env_snapshots", cfg.SnapshotCollection)
	}
	if cfg.ConnectTimeoutSeconds != 17 {
		t.Fatalf("cfg.ConnectTimeoutSeconds = %d, want 17", cfg.ConnectTimeoutSeconds)
	}
	if cfg.OperationTimeoutSeconds != 11 {
		t.Fatalf("cfg.OperationTimeoutSeconds = %d, want 11", cfg.OperationTimeoutSeconds)
	}
	if cfg.FlushIntervalSeconds != 41 {
		t.Fatalf("cfg.FlushIntervalSeconds = %d, want 41", cfg.FlushIntervalSeconds)
	}
}

func TestResolveConfigPath_UsesMainConfigDirectory(t *testing.T) {
	path := ResolveConfigPath("/tmp/cliproxy/config.yaml")
	if path != "/tmp/cliproxy/state-store.local.ini" {
		t.Fatalf("ResolveConfigPath() = %q, want /tmp/cliproxy/state-store.local.ini", path)
	}
}

func TestResolveConfigPaths_ConfigSpecificOnly(t *testing.T) {
	paths := ResolveConfigPaths("/tmp/cliproxy/config-277.yaml")
	if len(paths) != 1 {
		t.Fatalf("ResolveConfigPaths() len = %d, want 1", len(paths))
	}
	if paths[0] != "/tmp/cliproxy/state-store.277.ini" {
		t.Fatalf("ResolveConfigPaths()[0] = %q, want /tmp/cliproxy/state-store.277.ini", paths[0])
	}
}

func TestLoadRuntimeConfig_UsesConfigSpecificFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config-277.yaml")
	specificPath := filepath.Join(tempDir, "state-store.277.ini")
	if err := os.WriteFile(specificPath, []byte("[mongo]\nenabled = true\nuri = mongodb://specific:27017\n"), 0o600); err != nil {
		t.Fatalf("write specific state-store.277.ini: %v", err)
	}

	cfg, path, found, err := LoadRuntimeConfig(configPath)
	if err != nil {
		t.Fatalf("LoadRuntimeConfig() error = %v", err)
	}
	if !found {
		t.Fatal("LoadRuntimeConfig() found = false, want true")
	}
	if path != specificPath {
		t.Fatalf("LoadRuntimeConfig() path = %q, want %q", path, specificPath)
	}
	if cfg.URI != "mongodb://specific:27017" {
		t.Fatalf("cfg.URI = %q, want mongodb://specific:27017", cfg.URI)
	}
}
