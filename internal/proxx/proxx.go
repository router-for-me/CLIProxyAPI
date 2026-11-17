// Package proxx wires the fork-only runtime features (model groups, pricing, usage aggregator,
// request log viewer, self-update) into the
// service lifecycle through three one-line hooks in sdk/cliproxy. See docs/proxx/README.md.
package proxx

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/pricing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/requestlogs"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate"
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
	selfupdate.Start(ctx, cfg, configPath)
}

// Reload re-applies proxx.model-groups, proxx.pricing, proxx.usage and proxx.request-logs after a
// config change.
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
	applyDrainDefault(cfg)
	groups.Apply(cfg)
	pricing.SetAliases(cfg)
	pricing.Configure(ctx, cfg.Proxx.Pricing)
	if ctx != nil {
		usagestats.Configure(ctx, cfg.Proxx.Usage, configPath)
		requestlogs.Configure(ctx, cfg, configPath)
		selfupdate.Configure(cfg)
	}
}

// applyDrainDefault makes an unset (0) server.shutdown-timeout drain in-flight requests for up to
// 120s instead of closing them immediately (ADR 0007). A negative value keeps the immediate close.
func applyDrainDefault(cfg *config.Config) {
	if cfg.ShutdownTimeoutSeconds == 0 {
		cfg.ShutdownTimeoutSeconds = config.DefaultProxxShutdownTimeoutSeconds
	}
}

// ShutdownBudget bounds the whole service shutdown: the HTTP drain budget plus time for the remaining
// cleanup, and never less than upstream's 30s (hook in sdk/cliproxy/service_lifecycle.go).
func ShutdownBudget(cfg *config.Config) time.Duration {
	budget := 30 * time.Second
	if cfg != nil && cfg.ShutdownTimeoutSeconds > 0 {
		if d := time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second + 15*time.Second; d > budget {
			budget = d
		}
	}
	return budget
}

// Shutdown flushes the usage snapshot and stops the request log poller.
func Shutdown() {
	runtime.Lock()
	runtime.ctx = nil
	runtime.Unlock()
	requestlogs.Shutdown()
	usagestats.Shutdown()
}
