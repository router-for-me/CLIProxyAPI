package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseConfigBytes_CodexRuntimeDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`{}`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.Codex.StreamBootstrapBuffering {
		t.Fatal("default StreamBootstrapBuffering = false, want true")
	}
	if got := cfg.Codex.StreamBootstrapTimeoutDuration(); got != 30*time.Second {
		t.Fatalf("default StreamBootstrapTimeoutDuration() = %v, want 30s", got)
	}
	if got := cfg.DisableImageGeneration; got != DisableImageGenerationPassthrough {
		t.Fatalf("default DisableImageGeneration = %v, want passthrough", got)
	}
}

func TestParseConfigBytes_CodexRuntimeDefaultsCanBeOverridden(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
disable-image-generation: false
codex:
  stream-bootstrap-buffering: false
  stream-bootstrap-timeout: "0"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.Codex.StreamBootstrapBuffering {
		t.Fatal("StreamBootstrapBuffering = true, want explicit false")
	}
	if got := cfg.Codex.StreamBootstrapTimeoutDuration(); got != 0 {
		t.Fatalf("StreamBootstrapTimeoutDuration() = %v, want 0", got)
	}
	if got := cfg.DisableImageGeneration; got != DisableImageGenerationOff {
		t.Fatalf("DisableImageGeneration = %v, want false", got)
	}
}

func TestLoadConfigOptional_CodexHeaderDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex-header-defaults:
  user-agent: "  my-codex-client/1.0  "
  beta-features: "  feature-a,feature-b  "
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.CodexHeaderDefaults.UserAgent; got != "my-codex-client/1.0" {
		t.Fatalf("UserAgent = %q, want %q", got, "my-codex-client/1.0")
	}
	if got := cfg.CodexHeaderDefaults.BetaFeatures; got != "feature-a,feature-b" {
		t.Fatalf("BetaFeatures = %q, want %q", got, "feature-a,feature-b")
	}
	if cfg.Codex.DisableCodexCloaking {
		t.Fatal("DisableCodexCloaking = true, want default false")
	}
}

func TestLoadConfigOptional_CodexIdentityConfuse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  identity-confuse: true
  disable-codex-cloaking: true
  optimize-multi-agent-v2: true
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.Codex.IdentityConfuse {
		t.Fatalf("IdentityConfuse = false, want true")
	}
	if !cfg.Codex.DisableCodexCloaking {
		t.Fatal("DisableCodexCloaking = false, want true")
	}
	if !cfg.Codex.OptimizeMultiAgentV2 {
		t.Fatalf("OptimizeMultiAgentV2 = false, want true")
	}
}

func TestLoadConfigOptional_CodexModelLevelCooling(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  model-level-cooling: true
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.Codex.ModelLevelCooling {
		t.Fatalf("ModelLevelCooling = false, want true")
	}

	defaultCfg, errDefault := ParseConfigBytes([]byte(`{}`))
	if errDefault != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errDefault)
	}
	if defaultCfg.Codex.ModelLevelCooling {
		t.Fatalf("default ModelLevelCooling = true, want false")
	}
}
