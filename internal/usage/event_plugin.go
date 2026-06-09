package usage

import (
	"context"
	"os"
	"strconv"
	"strings"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultUsageEventLogDir        = "logs/usage"
	defaultUsageEventRetentionDays = 90
)

type usageEventSyncer interface {
	sync(ctx context.Context, event UsageEvent) error
}

type usageEventPlugin struct {
	writer *usageEventWriter
	syncer usageEventSyncer
}

func newUsageEventPlugin(writer *usageEventWriter, syncer usageEventSyncer) *usageEventPlugin {
	return &usageEventPlugin{writer: writer, syncer: syncer}
}

func (p *usageEventPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if strings.TrimSpace(record.APIKey) == "" {
		return
	}
	event := newUsageEvent(ctx, record)
	if p.writer != nil {
		if err := p.writer.write(event); err != nil {
			log.WithError(err).Warn("usage event write failed")
		}
	}
	if p.syncer != nil {
		if err := p.syncer.sync(ctx, event); err != nil {
			log.WithError(err).Warn("usage event sync failed")
		}
	}
}

func RegisterUsageEventPluginFromEnv() bool {
	plugin, enabled := newUsageEventPluginFromEnv()
	if !enabled || plugin == nil {
		return false
	}
	coreusage.RegisterPlugin(plugin)
	return true
}

func newUsageEventPluginFromEnv() (*usageEventPlugin, bool) {
	if !usageEventEnvEnabled(os.Getenv("USAGE_EVENTS_ENABLED")) {
		return nil, false
	}

	logDir := strings.TrimSpace(os.Getenv("USAGE_EVENTS_LOG_DIR"))
	if logDir == "" {
		logDir = defaultUsageEventLogDir
	}
	retentionDays := parseUsageEventRetentionDays(os.Getenv("USAGE_EVENTS_RETENTION_DAYS"))
	writer := newUsageEventWriter(logDir, retentionDays)

	syncer := usageEventSyncer(nil)
	url := strings.TrimSpace(os.Getenv("YUI_USAGE_EVENT_URL"))
	token := strings.TrimSpace(os.Getenv("YUI_USAGE_EVENT_TOKEN"))
	hmacSecret := strings.TrimSpace(os.Getenv("YUI_USAGE_EVENT_HMAC_SECRET"))
	if url != "" {
		if token == "" || hmacSecret == "" {
			log.Warn("usage event sync disabled because token or hmac secret is empty")
		} else {
			syncer = newUsageEventSyncClient(url, token, hmacSecret)
		}
	}

	return newUsageEventPlugin(writer, syncer), true
}

func usageEventEnvEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func parseUsageEventRetentionDays(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultUsageEventRetentionDays
	}
	days, err := strconv.Atoi(trimmed)
	if err != nil {
		log.WithError(err).Warn("invalid USAGE_EVENTS_RETENTION_DAYS, using default")
		return defaultUsageEventRetentionDays
	}
	return days
}
