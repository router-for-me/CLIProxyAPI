// Package proxx wires the fork-only runtime features (model groups, pricing, usage aggregator) into the
// service lifecycle through three one-line hooks in sdk/cliproxy. See docs/proxx/README.md.
package proxx

import (
	"context"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/pricing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/usagestats"
)

var runtime struct {
	sync.Mutex
	ctx        context.Context
	configPath string
}

// Start applies proxx config at service startup; ctx bounds background refresh/flush loops.
func Start(ctx context.Context, cfg *config.Config, configPath string) {
	runtime.Lock()
	runtime.ctx = ctx
	runtime.configPath = configPath
	runtime.Unlock()
	Reload(cfg)
}

// Reload re-applies proxx.model-groups, proxx.pricing and proxx.usage after a config change.
// Before Start it only stores prices (no background loops are started).
func Reload(cfg *config.Config) {
	if cfg == nil {
		return
	}
	runtime.Lock()
	ctx, configPath := runtime.ctx, runtime.configPath
	runtime.Unlock()
	if ctx != nil && ctx.Err() != nil {
		ctx = nil
	}
	groups.Apply(cfg)
	pricing.SetAliases(cfg)
	pricing.Configure(ctx, cfg.Proxx.Pricing)
	if ctx != nil {
		usagestats.Configure(ctx, cfg.Proxx.Usage, configPath)
	}
}

// Shutdown flushes the usage snapshot.
func Shutdown() {
	runtime.Lock()
	runtime.ctx = nil
	runtime.Unlock()
	usagestats.Shutdown()
}
