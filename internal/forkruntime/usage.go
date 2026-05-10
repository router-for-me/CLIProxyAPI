package forkruntime

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	log "github.com/sirupsen/logrus"
)

func ApplyUsageConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)
	redisqueue.SetUsageStatisticsEnabled(cfg.UsageStatisticsEnabled)
	redisqueue.SetRetentionSeconds(cfg.RedisUsageQueueRetentionSeconds)
}

func InitUsageStore(logDir string) {
	trimmedLogDir := strings.TrimSpace(logDir)
	if trimmedLogDir == "" {
		return
	}
	if err := usage.InitDefaultStoreInLogDir(trimmedLogDir); err != nil {
		log.WithError(err).Warn("usage store unavailable")
		return
	}
}

func CloseUsageStore() {
	if err := usage.CloseDefaultStore(); err != nil {
		log.WithError(err).Warn("usage store close failed")
	}
}
