package cliproxy

import (
	"context"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagestats"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// usageDrainTimeout bounds how long shutdown waits for queued usage records to
// reach their plugins. Draining normally takes milliseconds.
const usageDrainTimeout = 5 * time.Second

// drainUsageRecords waits for the records of requests that just finished to
// reach the store before it closes, without letting a stuck plugin hold up
// exit. It does not use the shutdown context: the HTTP drain
// (server.shutdown-timeout) runs first and can use that context up, and the
// requests it drains are the ones whose records are still queued.
func drainUsageRecords(m *usage.Manager) bool {
	ctx, cancel := context.WithTimeout(context.Background(), usageDrainTimeout)
	defer cancel()
	return m.Wait(ctx)
}

// applyUsageStatsConfig opens or closes the persistent usage statistics store
// to match usage-statistics-enabled. The database lives next to the
// credentials, at "<auth-dir>/usage.db". Home mode reports usage to Home
// instead, so nothing is stored locally there.
func (s *Service) applyUsageStatsConfig(cfg *config.Config) {
	enabled := cfg != nil && cfg.UsageStatisticsEnabled && !cfg.Home.Enabled
	path := ""
	if enabled {
		authDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
		if errResolve != nil {
			log.Errorf("usage statistics disabled: %v", errResolve)
			enabled = false
		} else {
			path = filepath.Join(authDir, usagestats.DefaultFileName)
		}
	}
	if errConfigure := usagestats.Configure(enabled, path); errConfigure != nil {
		log.Errorf("usage statistics disabled: %v", errConfigure)
	}
}
