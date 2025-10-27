package config_test

import (
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Ensure that presence of legacy key `claude-agent-sdk-for-python` does not break parsing
// after removal; loader should ignore unknown keys and proceed.
func TestConfig_IgnoresLegacyPythonBridgeKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `
port: 9001
claude-agent-sdk-for-python:
  enabled: true
  baseURL: "http://127.0.0.1:35332"
zhipu-api-key:
  - api-key: "glmsk-xyz"
    base-url: "https://open.bigmodel.cn/api/anthropic"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := appconfig.LoadConfigOptional(cfgPath, true)
	if err != nil {
		t.Fatalf("LoadConfigOptional returned error: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected config, got nil")
	}
	// No direct assertions on removed fields; success is that load did not error
}
