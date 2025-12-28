package alias

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewResolver(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "round-robin",
		Aliases: []config.ModelAlias{
			{
				Alias: "opus-4.5",
				Providers: []config.AliasProvider{
					{Provider: "antigravity", Model: "gemini-claude-opus-4-5"},
					{Provider: "kiro", Model: "kiro-claude-opus-4-5"},
				},
			},
		},
	}

	r := NewResolver(cfg)
	if r == nil {
		t.Fatal("expected non-nil resolver")
	}

	aliases := r.GetAliases()
	if len(aliases) != 1 {
		t.Errorf("expected 1 alias, got %d", len(aliases))
	}
}

func TestResolve(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "round-robin",
		Aliases: []config.ModelAlias{
			{
				Alias:    "opus-4.5",
				Strategy: "fill-first",
				Providers: []config.AliasProvider{
					{Provider: "antigravity", Model: "gemini-claude-opus-4-5"},
				},
			},
		},
	}

	r := NewResolver(cfg)

	// Test exact match
	resolved := r.Resolve("opus-4.5")
	if resolved == nil {
		t.Fatal("expected resolved alias")
	}
	if resolved.Strategy != "fill-first" {
		t.Errorf("expected strategy fill-first, got %s", resolved.Strategy)
	}

	// Test case-insensitive
	resolved = r.Resolve("OPUS-4.5")
	if resolved == nil {
		t.Fatal("expected case-insensitive match")
	}

	// Test non-alias
	resolved = r.Resolve("claude-sonnet-4")
	if resolved != nil {
		t.Error("expected nil for non-alias")
	}
}

func TestDefaultStrategy(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "fill-first",
		Aliases: []config.ModelAlias{
			{
				Alias: "test-model",
				// No strategy specified - should use default
				Providers: []config.AliasProvider{
					{Provider: "test", Model: "test-model-v1"},
				},
			},
		},
	}

	r := NewResolver(cfg)
	resolved := r.Resolve("test-model")
	if resolved == nil {
		t.Fatal("expected resolved alias")
	}
	if resolved.Strategy != "fill-first" {
		t.Errorf("expected default strategy fill-first, got %s", resolved.Strategy)
	}
}

func TestUpdate(t *testing.T) {
	r := NewResolver(nil)

	// Initially empty
	if len(r.GetAliases()) != 0 {
		t.Error("expected empty aliases initially")
	}

	// Update with config
	cfg := &config.ModelAliasConfig{
		Aliases: []config.ModelAlias{
			{
				Alias: "new-alias",
				Providers: []config.AliasProvider{
					{Provider: "test", Model: "test-model"},
				},
			},
		},
	}
	r.Update(cfg)

	if len(r.GetAliases()) != 1 {
		t.Error("expected 1 alias after update")
	}
}
