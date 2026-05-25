package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceAuthHookPersistsConfigBackedInsufficientBalanceDisable(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("claude-api-key:\n  - api-key: sk-test\n    base-url: https://claude.example.com\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	authID, _ := synthesizer.NewStableIDGenerator().Next("claude:apikey", "sk-test", "https://claude.example.com")
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:            authID,
		Provider:      "claude",
		Disabled:      true,
		Status:        coreauth.StatusDisabled,
		StatusMessage: "disabled due to insufficient balance",
		Attributes: map[string]string{
			"source":   "config:claude[test]",
			"api_key":  "sk-test",
			"base_url": "https://claude.example.com",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	service := &Service{
		cfg:         cfg,
		configPath:  configPath,
		coreManager: manager,
	}
	serviceAuthHook{service: service}.OnResult(context.Background(), coreauth.Result{
		AuthID:   authID,
		Provider: "claude",
		Model:    "claude-sonnet-4-6",
		Success:  false,
	})

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "disabled: true") {
		t.Fatalf("expected disabled state to be persisted, got:\n%s", string(data))
	}
	if !service.cfg.ClaudeKey[0].Disabled {
		t.Fatal("expected in-memory config to be disabled")
	}
}

func TestSetConfigBackedAuthDisabledStateMatchesOpenAICompatDuplicateByID(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "deepseek",
			BaseURL: "https://api.deepseek.example.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "sk-duplicate"},
				{APIKey: "sk-duplicate"},
			},
		}},
	}
	idGen := synthesizer.NewStableIDGenerator()
	firstID, _ := idGen.Next("openai-compatibility:deepseek", "sk-duplicate", "https://api.deepseek.example.com/v1", "")
	secondID, _ := idGen.Next("openai-compatibility:deepseek", "sk-duplicate", "https://api.deepseek.example.com/v1", "")
	if firstID == secondID {
		t.Fatal("expected duplicate entries to receive distinct stable IDs")
	}

	changed := setConfigBackedAuthDisabledState(cfg, &coreauth.Auth{
		ID:       secondID,
		Provider: "deepseek",
		Attributes: map[string]string{
			"source":      "config:deepseek[test]",
			"api_key":     "sk-duplicate",
			"base_url":    "https://api.deepseek.example.com/v1",
			"compat_name": "deepseek",
		},
	}, true)
	if !changed {
		t.Fatal("expected config to change")
	}
	if cfg.OpenAICompatibility[0].APIKeyEntries[0].Disabled {
		t.Fatal("first duplicate entry should remain enabled")
	}
	if !cfg.OpenAICompatibility[0].APIKeyEntries[1].Disabled {
		t.Fatal("target duplicate entry should be disabled")
	}
}
