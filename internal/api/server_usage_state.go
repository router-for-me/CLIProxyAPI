package api

import (
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usageaggregate"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestate"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// tokenAggregateSource adapts the process-wide token aggregate to a
// usagestate.TokensSource.
type tokenAggregateSource struct{}

func (tokenAggregateSource) Revision() uint64 { return usageaggregate.Revision() }

func (tokenAggregateSource) Snapshot() usageaggregate.UsageSnapshot {
	return usageaggregate.Snapshot()
}

func (tokenAggregateSource) Restore(snapshot usageaggregate.UsageSnapshot) {
	usageaggregate.Restore(snapshot)
}

// newUsageState builds the single-file usage-state persistence manager. It
// returns nil when persistence is unavailable so callers can skip it entirely.
func newUsageState(cfg *config.Config, authManager *coreauth.Manager) *usagestate.Manager {
	if cfg == nil || authManager == nil {
		return nil
	}
	authDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil || authDir == "" {
		log.Warnf("usage state persistence disabled: resolve auth dir: %v", errResolve)
		return nil
	}

	manager := usagestate.New(
		usagestate.UsageStatePath(filepath.Clean(authDir)),
		usagestate.DefaultInterval,
		authManager,
		tokenAggregateSource{},
	)
	if errLoad := manager.Load(); errLoad != nil {
		log.Warnf("failed to load persisted usage state: %v", errLoad)
	}
	return manager
}
