package config_test

import (
    "os"
    "path/filepath"
    "testing"

    appconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Test that Claude Agent SDK for Python (config key: claude-agent-sdk-for-python) enabled/baseURL are parsed and defaults/trim applied
func TestPythonAgentConfig_ParseAndPriority(t *testing.T) {
    dir := t.TempDir()
    cfgPath := filepath.Join(dir, "config.yaml")
    yaml := `
port: 9001
claude-agent-sdk-for-python:
  enabled: true
  baseURL: " http://127.0.0.1:35332 "
zhipu-api-key:
  - api-key: "glmsk-xyz"
    base-url: "https://open.bigmodel.cn/api/anthropic"
`
    if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
        t.Fatalf("write temp config: %v", err)
    }

    // Set env that should NOT override config parsing values (executor will decide precedence)
    t.Setenv("CLAUDE_AGENT_SDK_URL", "http://127.0.0.1:39999")

    cfg, err := appconfig.LoadConfigOptional(cfgPath, true)
    if err != nil {
        t.Fatalf("LoadConfigOptional returned error: %v", err)
    }
    if cfg == nil {
        t.Fatalf("expected config, got nil")
    }

    if !cfg.PythonAgent.Enabled {
        t.Fatalf("expected claude-agent-sdk-for-python.enabled=true, got false")
    }
    want := "http://127.0.0.1:35332"
    if cfg.PythonAgent.BaseURL != want {
        t.Fatalf("expected claude-agent-sdk-for-python.baseURL=%q after trim, got %q", want, cfg.PythonAgent.BaseURL)
    }
}

// Test that when Claude Agent SDK for Python is disabled (claude-agent-sdk-for-python.enabled=false),
// the disabled state is respected by config loader (executor fallback is validated elsewhere).
func TestPythonAgentConfig_DisabledFallbackFlag(t *testing.T) {
    dir := t.TempDir()
    cfgPath := filepath.Join(dir, "config.yaml")
    yaml := `
port: 9002
claude-agent-sdk-for-python:
  enabled: false
  baseURL: "http://127.0.0.1:35333"
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

    if cfg.PythonAgent.Enabled {
        t.Fatalf("expected claude-agent-sdk-for-python.enabled=false, got true")
    }
    if cfg.PythonAgent.BaseURL != "http://127.0.0.1:35333" {
        t.Fatalf("expected claude-agent-sdk-for-python.baseURL to parse, got %q", cfg.PythonAgent.BaseURL)
    }
}
