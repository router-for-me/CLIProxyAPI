package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const homePluginStatusReportTimeout = 10 * time.Second

func (s *Service) syncHomePlugins(ctx context.Context, cfg *config.Config) (homeplugins.SyncReport, string, bool, error) {
	if s == nil || cfg == nil || !cfg.Home.Enabled {
		return homeplugins.SyncReport{}, "", false, nil
	}
	syncKey := homePluginSyncKey(cfg)
	if syncKey != "" {
		s.homePluginSyncMu.Lock()
		if s.homePluginSyncKey == syncKey {
			s.homePluginSyncMu.Unlock()
			return homeplugins.SyncReport{}, syncKey, false, nil
		}
		s.homePluginSyncMu.Unlock()
	}
	report, errSync := homeplugins.SyncWithReport(ctx, cfg, s.pluginHost)
	return report, syncKey, true, errSync
}

func (s *Service) markHomePluginsSynced(syncKey string) {
	if s == nil || strings.TrimSpace(syncKey) == "" {
		return
	}
	s.homePluginSyncMu.Lock()
	s.homePluginSyncKey = syncKey
	s.homePluginSyncMu.Unlock()
}

func (s *Service) reportHomePluginStatus(ctx context.Context, cfg *config.Config, report homeplugins.SyncReport) {
	if s == nil || cfg == nil {
		return
	}
	if s.homeClient == nil {
		log.Warn("failed to report home plugin status: home client is unavailable")
		return
	}
	nodeID := strings.TrimSpace(cfg.Home.NodeID)
	if nodeID == "" {
		log.Warn("failed to report home plugin status: node id is empty")
		return
	}
	report.NodeID = nodeID
	report.UpdatedAt = time.Now().UTC()
	raw, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		log.Warnf("failed to marshal home plugin status: %v", errMarshal)
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reportCtx, cancel := context.WithTimeout(ctx, homePluginStatusReportTimeout)
	defer cancel()
	if errReport := s.homeClient.RPushPluginStatus(reportCtx, raw); errReport != nil {
		log.Warnf("failed to report home plugin status: %v", errReport)
	}
}

func (s *Service) processHomePluginTasks(ctx context.Context, cfg *config.Config) {
	if s == nil || cfg == nil || !cfg.Home.Enabled || s.homeClient == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tasks, errTasks := s.homeClient.GetPluginTasks(ctx)
	if errTasks != nil {
		log.Warnf("failed to fetch home plugin tasks: %v", errTasks)
		return
	}
	for _, task := range tasks {
		if !strings.EqualFold(strings.TrimSpace(task.Operation), "delete") {
			continue
		}
		report := s.processHomePluginDeleteTask(ctx, cfg, task)
		if !report.OK && strings.TrimSpace(report.Error) != "" {
			log.Warnf("failed to process home plugin delete task %d for %s: %v", task.ID, task.PluginID, report.Error)
		}
		s.reportHomePluginStatus(ctx, cfg, report)
	}
}

func (s *Service) processHomePluginDeleteTask(ctx context.Context, cfg *config.Config, task home.PluginTask) homeplugins.SyncReport {
	return homeplugins.DeleteWithReport(ctx, cfg, s.pluginHost, task.ID, task.PluginID)
}

func homePluginSyncKey(cfg *config.Config) string {
	if cfg == nil || !cfg.Home.Enabled {
		return ""
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "enabled=%t\ndir=%s\n", cfg.Plugins.Enabled, strings.TrimSpace(cfg.Plugins.Dir))
	ids := make([]string, 0, len(cfg.Plugins.Configs))
	for id := range cfg.Plugins.Configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := cfg.Plugins.Configs[id]
		enabled := false
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		_, _ = fmt.Fprintf(hash, "plugin=%s\nenabled=%t\npriority=%d\n", strings.TrimSpace(id), enabled, item.Priority)
		if item.Raw.Kind != 0 {
			raw, errMarshal := yaml.Marshal(&item.Raw)
			if errMarshal == nil {
				_, _ = hash.Write(raw)
			}
		}
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
