package cliproxy

import (
	"sync"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/cachestats"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

// cacheStatsHookOnce guards the one-time registration of the usage hook. The
// hook reads the process-wide store, which ApplyConfig reconfigures in place,
// so the registration itself never has to be replaced.
var (
	cacheStatsHookOnce   sync.Once
	cacheStatsAnnounceMu sync.Mutex
	cacheStatsAnnounced  *internalconfig.UsageCacheStatsConfig
)

// announceCacheStats reports whether the settings changed since the last call,
// so startup and the config-runtime pass that follows it do not log twice.
func announceCacheStats(settings internalconfig.UsageCacheStatsConfig) bool {
	cacheStatsAnnounceMu.Lock()
	defer cacheStatsAnnounceMu.Unlock()
	previous := cacheStatsAnnounced
	cacheStatsAnnounced = &settings
	if previous == nil {
		return true
	}
	return previous.Enabled != settings.Enabled ||
		previous.MaxSessions != settings.MaxSessions ||
		previous.PerSessionRequests != settings.PerSessionRequests ||
		previous.IdleTTL != settings.IdleTTL
}

// applyCacheStatsConfig installs or reconfigures the per-session prompt-cache
// statistics store. It is safe to call on every config reload.
func (s *Service) applyCacheStatsConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	settings := internalconfig.UsageCacheStatsConfig{}
	if cfg != nil {
		settings = cfg.UsageCacheStats
	}
	settings = settings.WithDefaults()

	cacheStatsHookOnce.Do(func() { cachestats.RegisterUsagePlugin(nil) })
	cachestats.Default().ApplyConfig(cachestats.Config{
		Enabled:            settings.Enabled,
		MaxSessions:        settings.MaxSessions,
		PerSessionRequests: settings.PerSessionRequests,
		IdleTTL:            settings.IdleTTL,
	})

	if !announceCacheStats(settings) {
		return
	}
	if !settings.Enabled {
		log.Info("cache-stats: disabled")
		return
	}
	log.Infof("cache-stats: enabled | max-sessions=%d per-session-requests=%d idle-ttl=%s",
		settings.MaxSessions, settings.PerSessionRequests, settings.IdleTTL)
}
