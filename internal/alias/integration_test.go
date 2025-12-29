//go:build integration

package alias

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGlobalResolverIntegration(t *testing.T) {
	cfg := &config.ModelAliasConfig{
		DefaultStrategy: "round-robin",
		Aliases: []config.ModelAlias{
			{
				Alias: "test-alias",
				Providers: []config.AliasProvider{
					{Provider: "test-provider", Model: "test-model"},
				},
			},
		},
	}

	InitGlobalResolver(cfg)

	r := GetGlobalResolver()
	if r == nil {
		t.Fatal("expected global resolver")
	}

	resolved := r.Resolve("test-alias")
	if resolved == nil {
		t.Fatal("expected resolved alias")
	}

	// Test update
	newCfg := &config.ModelAliasConfig{
		Aliases: []config.ModelAlias{
			{
				Alias: "new-alias",
				Providers: []config.AliasProvider{
					{Provider: "new-provider", Model: "new-model"},
				},
			},
		},
	}
	UpdateGlobalResolver(newCfg)

	resolved = r.Resolve("new-alias")
	if resolved == nil {
		t.Fatal("expected new alias after update")
	}
}
