package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_OpenAICompatNetworkRetryDefaults(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.OpenAICompatNetworkRetry != 1 {
		t.Fatalf("openai-compat-network-retry = %d, want 1", cfg.OpenAICompatNetworkRetry)
	}
	if cfg.OpenAICompatNetworkRetryBackoffMS != 500 {
		t.Fatalf("openai-compat-network-retry-backoff-ms = %d, want 500", cfg.OpenAICompatNetworkRetryBackoffMS)
	}
}

func TestLoadConfigOptional_OpenAICompatNetworkRetryNegativeSanitized(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	content := []byte("openai-compat-network-retry: -1\nopenai-compat-network-retry-backoff-ms: -5\n")
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.OpenAICompatNetworkRetry != 0 {
		t.Fatalf("openai-compat-network-retry = %d, want 0", cfg.OpenAICompatNetworkRetry)
	}
	if cfg.OpenAICompatNetworkRetryBackoffMS != 0 {
		t.Fatalf("openai-compat-network-retry-backoff-ms = %d, want 0", cfg.OpenAICompatNetworkRetryBackoffMS)
	}
}
