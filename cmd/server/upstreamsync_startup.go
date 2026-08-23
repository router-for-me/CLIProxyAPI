package main

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/upstreamsync"
)

// applyPersistedUpstreamProviders makes the normalized upstream_providers table
// the source of truth on every startup, not only after a management mutation.
// In particular, this preserves each Claude API-key row's UpstreamProviderID
// through config rendering so the runtime registry can publish claude:<rowID>
// rather than collapsing all rows onto bare claude.
func applyPersistedUpstreamProviders(ctx context.Context, providers store.UpstreamProviderStore, cfg *config.Config, configPath, authDir string) (*config.Config, error) {
	if providers == nil || cfg == nil {
		return cfg, nil
	}
	return upstreamsync.ApplyArtifacts(ctx, providers, cfg, configPath, authDir)
}
