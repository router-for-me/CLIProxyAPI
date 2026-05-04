package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveConfigPreserveCommentsMatchesSequenceEntriesByCompositeIdentity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	original := `claude-api-key:
  # a entry
  - api-key: shared-key
    base-url: https://a.example.com
  # b entry
  - api-key: shared-key
    base-url: https://b.example.com
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{APIKey: "shared-key", BaseURL: "https://b.example.com", Disabled: true},
			{APIKey: "shared-key", BaseURL: "https://a.example.com"},
		},
	}
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments failed: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	text := string(data)
	want := "# b entry\n  - api-key: shared-key\n    base-url: https://b.example.com\n    disabled: true"
	if !strings.Contains(text, want) {
		t.Fatalf("expected b comment to stay with b entry after reorder; want block:\n%s\n\ngot:\n%s", want, text)
	}
}

func TestSanitizeClaudeKeysDropsEmptyAPIKeys(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{APIKey: "   ", RoutingGroup: "orphan"},
			{APIKey: " sk-live ", BaseURL: " https://claude.example.com ", ProxyURL: " http://proxy.example.com ", RoutingGroup: " team-a "},
		},
	}

	cfg.SanitizeClaudeKeys()

	if got := len(cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude-api-key len = %d, want 1", got)
	}
	entry := cfg.ClaudeKey[0]
	if entry.APIKey != "sk-live" {
		t.Fatalf("api-key = %q, want sk-live", entry.APIKey)
	}
	if entry.BaseURL != "https://claude.example.com" {
		t.Fatalf("base-url = %q, want https://claude.example.com", entry.BaseURL)
	}
	if entry.ProxyURL != "http://proxy.example.com" {
		t.Fatalf("proxy-url = %q, want http://proxy.example.com", entry.ProxyURL)
	}
	if entry.RoutingGroup != "team-a" {
		t.Fatalf("routing-group = %q, want team-a", entry.RoutingGroup)
	}
}
