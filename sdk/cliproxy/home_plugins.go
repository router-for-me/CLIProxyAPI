package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const homePluginStatusReportTimeout = 10 * time.Second

type homePluginStatusWork struct {
	cfg    *config.Config
	report homeplugins.SyncReport
}

type homePluginTaskWork struct {
	cfg    *config.Config
	task   home.PluginTask
	report *homeplugins.SyncReport
}

type homePluginFinalization struct {
	config       *config.Config
	configCommit configCommit
	committed    bool
	statusWork   []homePluginStatusWork
	nextStatus   int
	taskWork     []homePluginTaskWork
	nextTask     int
	syncKey      string
	markSynced   bool
}

func (s *Service) syncHomePlugins(ctx context.Context, cfg *config.Config) (homeplugins.SyncReport, string, bool, error) {
	return s.syncHomePluginsWithClient(ctx, cfg, nil)
}

func (s *Service) syncHomePluginsWithClient(ctx context.Context, cfg *config.Config, client *home.Client) (homeplugins.SyncReport, string, bool, error) {
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
	if !cfg.Plugins.Enabled {
		return homeplugins.CompletedSyncReport(homeplugins.CurrentPlatform(), nil), syncKey, false, nil
	}
	installedVersions, errInstalled := homeplugins.InstalledVersions(cfg)
	if errInstalled != nil {
		return homeplugins.CompletedSyncReport(homeplugins.CurrentPlatform(), errInstalled), syncKey, false, errInstalled
	}
	platform := homeplugins.CurrentPlatform()
	request := sdkpluginstore.PluginSyncRequest{
		SchemaVersion:     sdkpluginstore.PluginSyncSchemaVersion,
		GOOS:              platform.GOOS,
		GOARCH:            platform.GOARCH,
		InstalledVersions: installedVersions,
	}
	defer request.Clear()
	response, errFetch := s.fetchHomePluginSyncWithClient(ctx, client, request)
	if errors.Is(errFetch, home.ErrPluginSyncUnsupported) {
		response.Clear()
		report, errSync := homeplugins.SyncWithReport(ctx, cfg, s.pluginHost)
		return report, syncKey, true, errSync
	}
	if errFetch != nil {
		return homeplugins.CompletedSyncReport(platform, errFetch), syncKey, false, errFetch
	}
	defer response.Clear()
	report, errSync := homeplugins.SyncResolvedWithReport(ctx, cfg, response.Items, response.ExpiresAt, request.InstalledVersions, s.pluginHost)
	return report, syncKey, true, errSync
}

func (s *Service) fetchHomePluginSyncWithClient(ctx context.Context, client *home.Client, request sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
	if s.homePluginSyncFetch != nil {
		return s.homePluginSyncFetch(ctx, request)
	}
	if client == nil {
		s.homeMu.Lock()
		client = s.homeClient
		s.homeMu.Unlock()
	}
	if client == nil {
		return sdkpluginstore.PluginSyncResponse{}, fmt.Errorf("home client is unavailable")
	}
	return client.GetPluginSync(ctx, request)
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
	s.reportHomePluginStatusWithClient(ctx, cfg, report, nil)
}

func (s *Service) reportHomePluginStatusWithClient(ctx context.Context, cfg *config.Config, report homeplugins.SyncReport, client *home.Client) {
	if errReport := s.pushHomePluginStatusWithClient(ctx, cfg, report, client); errReport != nil {
		log.Warnf("failed to report home plugin status: %v", errReport)
	}
}

func (s *Service) pushHomePluginStatusWithClient(ctx context.Context, cfg *config.Config, report homeplugins.SyncReport, client *home.Client) error {
	if s == nil || cfg == nil {
		return nil
	}
	if client == nil {
		s.homeMu.Lock()
		client = s.homeClient
		s.homeMu.Unlock()
	}
	if client == nil {
		return fmt.Errorf("home client is unavailable")
	}
	nodeID := strings.TrimSpace(cfg.Home.NodeID)
	if nodeID == "" {
		return fmt.Errorf("home node id is empty")
	}
	report.NodeID = nodeID
	report.UpdatedAt = time.Now().UTC()
	raw, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		return fmt.Errorf("marshal home plugin status: %w", errMarshal)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reportCtx, cancel := context.WithTimeout(ctx, homePluginStatusReportTimeout)
	defer cancel()
	if errReport := client.RPushPluginStatus(reportCtx, raw); errReport != nil {
		return fmt.Errorf("push home plugin status: %w", errReport)
	}
	return nil
}

func (s *Service) processHomePluginTasks(ctx context.Context, cfg *config.Config) {
	s.processHomePluginTasksWithClient(ctx, cfg, nil)
}

func (s *Service) processHomePluginTasksWithClient(ctx context.Context, cfg *config.Config, client *home.Client) {
	tasks, errStage := s.stageHomePluginTasksWithClient(ctx, cfg, client)
	if errStage != nil {
		log.Warnf("failed to fetch home plugin tasks: %v", errStage)
		return
	}
	work := &homePluginFinalization{taskWork: tasks}
	if errFinalize := s.finalizeHomePluginWork(ctx, client, work); errFinalize != nil {
		log.Warnf("failed to finalize home plugin tasks: %v", errFinalize)
	}
}

func (s *Service) stageHomePluginTasksWithClient(ctx context.Context, cfg *config.Config, client *home.Client) ([]homePluginTaskWork, error) {
	if s == nil || cfg == nil || !cfg.Home.Enabled {
		return nil, nil
	}
	if client == nil {
		s.homeMu.Lock()
		client = s.homeClient
		s.homeMu.Unlock()
	}
	if client == nil {
		return nil, fmt.Errorf("home client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tasks, errTasks := client.GetPluginTasks(ctx)
	if errTasks != nil {
		return nil, errTasks
	}
	staged := make([]homePluginTaskWork, 0, len(tasks))
	for _, task := range tasks {
		if !strings.EqualFold(strings.TrimSpace(task.Operation), "delete") {
			continue
		}
		staged = append(staged, homePluginTaskWork{cfg: cfg, task: task})
	}
	return staged, nil
}

func (s *Service) finalizeHomePluginWork(ctx context.Context, client *home.Client, work *homePluginFinalization) error {
	if work == nil {
		return nil
	}
	if ctx != nil {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
	}
	for work.nextStatus < len(work.statusWork) {
		status := work.statusWork[work.nextStatus]
		if errReport := s.pushHomePluginStatusWithClient(ctx, status.cfg, status.report, client); errReport != nil {
			return errReport
		}
		work.nextStatus++
	}
	for work.nextTask < len(work.taskWork) {
		taskWork := &work.taskWork[work.nextTask]
		if taskWork.report == nil {
			report := s.processHomePluginDeleteTask(ctx, taskWork.cfg, taskWork.task)
			taskWork.report = &report
			if !report.OK && strings.TrimSpace(report.Error) != "" {
				log.Warnf("failed to process home plugin delete task %d for %s: %v", taskWork.task.ID, taskWork.task.PluginID, report.Error)
			}
		}
		if errReport := s.pushHomePluginStatusWithClient(ctx, taskWork.cfg, *taskWork.report, client); errReport != nil {
			return errReport
		}
		work.nextTask++
	}
	if work.markSynced {
		if ctx != nil {
			if errContext := ctx.Err(); errContext != nil {
				return errContext
			}
		}
		s.markHomePluginsSynced(work.syncKey)
		work.markSynced = false
	}
	return nil
}

func (s *Service) processHomePluginDeleteTask(ctx context.Context, cfg *config.Config, task home.PluginTask) homeplugins.SyncReport {
	if s != nil && s.homePluginDeleteTask != nil {
		return s.homePluginDeleteTask(ctx, cfg, task)
	}
	return homeplugins.DeleteWithReport(ctx, cfg, s.pluginHost, task.ID, task.PluginID)
}

func homePluginSyncKey(cfg *config.Config) string {
	if cfg == nil || !cfg.Home.Enabled {
		return ""
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "enabled=%t\ndir=%s\nauth-revision=%d\n", cfg.Plugins.Enabled, strings.TrimSpace(cfg.Plugins.Dir), cfg.Plugins.AuthRevision)
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
