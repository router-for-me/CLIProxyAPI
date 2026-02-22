package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func TestResolveOAuthBaseURLWithOverride_PreferenceOrder(t *testing.T) {
	cfg := &config.Config{
		OAuthUpstream: map[string]string{
			"claude": "https://cfg.example.com/claude",
		},
	}

	got := resolveOAuthBaseURLWithOverride(cfg, "claude", "https://default.example.com", "https://auth.example.com")
	if got != "https://auth.example.com" {
		t.Fatalf("expected auth override to win, got %q", got)
	}

	got = resolveOAuthBaseURLWithOverride(cfg, "claude", "https://default.example.com", "")
	if got != "https://cfg.example.com/claude" {
		t.Fatalf("expected config override to win when auth override missing, got %q", got)
	}

	got = resolveOAuthBaseURLWithOverride(cfg, "codex", "https://default.example.com/", "")
	if got != "https://default.example.com" {
		t.Fatalf("expected default URL fallback when no overrides exist, got %q", got)
	}
}
