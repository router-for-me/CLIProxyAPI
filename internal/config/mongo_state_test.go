package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_MongoStateDefaultsAppliedIndependently(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	content := []byte("panel-github-repository: \"\"\n")
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if cfg.MongoState.Enabled {
		t.Fatalf("mongo-state.enabled = true, want false")
	}
	if cfg.MongoState.Database != "cliproxy_state" {
		t.Fatalf("mongo-state.database = %q, want %q", cfg.MongoState.Database, "cliproxy_state")
	}
	if cfg.MongoState.SnapshotCollection != "service_state_snapshots" {
		t.Fatalf("mongo-state.snapshot-collection = %q, want %q", cfg.MongoState.SnapshotCollection, "service_state_snapshots")
	}
	if cfg.MongoState.ConnectTimeoutSeconds != 10 {
		t.Fatalf("mongo-state.connect-timeout-seconds = %d, want 10", cfg.MongoState.ConnectTimeoutSeconds)
	}
	if cfg.MongoState.OperationTimeoutSeconds != 5 {
		t.Fatalf("mongo-state.operation-timeout-seconds = %d, want 5", cfg.MongoState.OperationTimeoutSeconds)
	}
	if cfg.MongoState.FlushIntervalSeconds != 30 {
		t.Fatalf("mongo-state.flush-interval-seconds = %d, want 30", cfg.MongoState.FlushIntervalSeconds)
	}
}

func TestLoadConfigOptional_MongoStateExplicitConfigPreservedWhenPanelRepositoryEmpty(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	content := []byte(`
panel-github-repository: ""
mongo-state:
  enabled: true
  uri: "mongodb://127.0.0.1:27017"
  database: "custom_state"
  snapshot-collection: "custom_snapshots"
  connect-timeout-seconds: 21
  operation-timeout-seconds: 8
  flush-interval-seconds: 99
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.MongoState.Enabled {
		t.Fatalf("mongo-state.enabled = false, want true")
	}
	if cfg.MongoState.URI != "mongodb://127.0.0.1:27017" {
		t.Fatalf("mongo-state.uri = %q, want explicit value", cfg.MongoState.URI)
	}
	if cfg.MongoState.Database != "custom_state" {
		t.Fatalf("mongo-state.database = %q, want %q", cfg.MongoState.Database, "custom_state")
	}
	if cfg.MongoState.SnapshotCollection != "custom_snapshots" {
		t.Fatalf("mongo-state.snapshot-collection = %q, want %q", cfg.MongoState.SnapshotCollection, "custom_snapshots")
	}
	if cfg.MongoState.ConnectTimeoutSeconds != 21 {
		t.Fatalf("mongo-state.connect-timeout-seconds = %d, want 21", cfg.MongoState.ConnectTimeoutSeconds)
	}
	if cfg.MongoState.OperationTimeoutSeconds != 8 {
		t.Fatalf("mongo-state.operation-timeout-seconds = %d, want 8", cfg.MongoState.OperationTimeoutSeconds)
	}
	if cfg.MongoState.FlushIntervalSeconds != 99 {
		t.Fatalf("mongo-state.flush-interval-seconds = %d, want 99", cfg.MongoState.FlushIntervalSeconds)
	}
}

func TestLoadConfigOptional_MongoStateYamlShape(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	content := []byte(`
mongo-state:
  enabled: true
  uri: "mongodb://127.0.0.1:27017"
  database: "cliproxy_state"
  snapshot-collection: "service_state_snapshots"
  connect-timeout-seconds: 10
  operation-timeout-seconds: 5
  flush-interval-seconds: 30
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.MongoState.Enabled {
		t.Fatalf("mongo-state.enabled = false, want true")
	}
	if cfg.MongoState.Database != "cliproxy_state" {
		t.Fatalf("mongo-state.database = %q, want %q", cfg.MongoState.Database, "cliproxy_state")
	}
	if cfg.MongoState.SnapshotCollection != "service_state_snapshots" {
		t.Fatalf("mongo-state.snapshot-collection = %q, want %q", cfg.MongoState.SnapshotCollection, "service_state_snapshots")
	}
}
