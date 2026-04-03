package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type stubTokenClientProvider struct{}

func (stubTokenClientProvider) Load(context.Context, *config.Config) (*TokenClientResult, error) {
	return &TokenClientResult{}, nil
}

type stubAPIKeyClientProvider struct{}

func (stubAPIKeyClientProvider) Load(context.Context, *config.Config) (*APIKeyClientResult, error) {
	return &APIKeyClientResult{}, nil
}

func replaceUsageStatisticsSnapshot(snapshot internalusage.StatisticsSnapshot) {
	stats := internalusage.GetRequestStatistics()
	*stats = *internalusage.NewRequestStatistics()
	stats.MergeSnapshot(snapshot)
}

func TestServiceShutdownPersistsUsageSnapshot(t *testing.T) {
	tempDir := t.TempDir()
	snapshotPath := filepath.Join(tempDir, "usage-statistics.json")

	cfg := &config.Config{
		UsageStatisticsEnabled:            true,
		UsageStatisticsPersistenceEnabled: true,
		UsageStatisticsPersistenceFile:    snapshotPath,
	}

	originalStatisticsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	defer internalusage.SetStatisticsEnabled(originalStatisticsEnabled)

	stats := internalusage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "api-key",
		Model:       "gpt-5.4",
		Source:      "test",
		AuthIndex:   "0",
		RequestedAt: time.Date(2026, time.April, 2, 12, 0, 0, 0, time.UTC),
		Latency:     500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 2,
			TotalTokens:  5,
		},
	})

	service := &Service{
		cfg:              cfg,
		configPath:       filepath.Join(tempDir, "config.yaml"),
		usagePersistence: internalusage.NewPersistenceManager(cfg, filepath.Join(tempDir, "config.yaml"), stats),
	}

	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v, want nil", err)
	}

	var snapshot struct {
		Version int `json:"version"`
		Usage   struct {
			TotalRequests int64 `json:"total_requests"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}
	if snapshot.Version != 1 {
		t.Fatalf("version = %d, want 1", snapshot.Version)
	}
	if snapshot.Usage.TotalRequests != 1 {
		t.Fatalf("usage.total_requests = %d, want 1", snapshot.Usage.TotalRequests)
	}
}

func TestServiceRunInitializesUsagePersistenceWhenEnabled(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	authDir := filepath.Join(tempDir, "auth")
	snapshotPath := filepath.Join(tempDir, "usage-statistics.json")

	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v, want nil", err)
	}
	if err := os.WriteFile(configPath, []byte("host: 127.0.0.1\nport: 0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want nil", err)
	}

	cfg := &config.Config{
		Host:                              "127.0.0.1",
		Port:                              0,
		AuthDir:                           authDir,
		UsageStatisticsEnabled:            true,
		UsageStatisticsPersistenceEnabled: true,
		UsageStatisticsPersistenceFile:    snapshotPath,
		CommercialMode:                    true,
	}

	originalStatisticsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	defer internalusage.SetStatisticsEnabled(originalStatisticsEnabled)

	originalSnapshot := internalusage.GetRequestStatistics().Snapshot()
	replaceUsageStatisticsSnapshot(internalusage.StatisticsSnapshot{})
	defer replaceUsageStatisticsSnapshot(originalSnapshot)

	uniqueSuffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	apiKey := "startup-api-key-" + uniqueSuffix
	model := "gpt-5.4-" + uniqueSuffix

	seedStats := internalusage.NewRequestStatistics()
	seedStats.Record(context.Background(), coreusage.Record{
		APIKey:      apiKey,
		Model:       model,
		Source:      "startup-test",
		AuthIndex:   "startup-auth-" + uniqueSuffix,
		RequestedAt: time.Date(2026, time.April, 2, 13, 0, 0, 0, time.UTC),
		Latency:     250 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 2,
			TotalTokens:  5,
		},
	})
	seedManager := internalusage.NewPersistenceManager(cfg, configPath, seedStats)
	if err := seedManager.Save(); err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}

	var hookErr error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service, err := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithTokenClientProvider(stubTokenClientProvider{}).
		WithAPIKeyClientProvider(stubAPIKeyClientProvider{}).
		WithWatcherFactory(func(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{}, nil
		}).
		WithHooks(Hooks{OnAfterStart: func(s *Service) {
			if s.usagePersistence == nil {
				hookErr = fmt.Errorf("usagePersistence = nil, want initialized manager")
				cancel()
				return
			}

			snapshot := internalusage.GetRequestStatistics().Snapshot()
			statsKey := apiKey
			apiSnapshot, ok := snapshot.APIs[statsKey]
			if !ok {
				hookErr = fmt.Errorf("restored snapshot missing API key entry")
				cancel()
				return
			}
			modelSnapshot, ok := apiSnapshot.Models[model]
			if !ok || modelSnapshot.TotalRequests == 0 {
				hookErr = fmt.Errorf("restored snapshot missing model entry")
			}
			cancel()
		}}).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v, want nil", err)
	}

	if err := service.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
}

func TestServiceReloadRestoresUsageSnapshotBeforeSavingNewPersistenceTarget(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	authDir := filepath.Join(tempDir, "auth")
	oldSnapshotPath := filepath.Join(tempDir, "usage-old.json")
	newSnapshotPath := filepath.Join(tempDir, "usage-new.json")

	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v, want nil", err)
	}
	if err := os.WriteFile(configPath, []byte("host: 127.0.0.1\nport: 0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want nil", err)
	}

	baseCfg := &config.Config{
		Host:                              "127.0.0.1",
		Port:                              0,
		AuthDir:                           authDir,
		UsageStatisticsEnabled:            true,
		UsageStatisticsPersistenceEnabled: true,
		UsageStatisticsPersistenceFile:    oldSnapshotPath,
		CommercialMode:                    true,
	}

	originalStatisticsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	defer internalusage.SetStatisticsEnabled(originalStatisticsEnabled)

	originalSnapshot := internalusage.GetRequestStatistics().Snapshot()
	replaceUsageStatisticsSnapshot(internalusage.StatisticsSnapshot{})
	defer replaceUsageStatisticsSnapshot(originalSnapshot)

	liveStats := internalusage.GetRequestStatistics()
	liveKey := "reload-live-api-key"
	liveModel := "gpt-live"
	liveStats.Record(context.Background(), coreusage.Record{
		APIKey:      liveKey,
		Model:       liveModel,
		Source:      "reload-live",
		AuthIndex:   "live-auth",
		RequestedAt: time.Date(2026, time.April, 2, 14, 0, 0, 0, time.UTC),
		Latency:     200 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  2,
			OutputTokens: 3,
			TotalTokens:  5,
		},
	})

	persistedStats := internalusage.NewRequestStatistics()
	persistedKey := "reload-persisted-api-key"
	persistedModel := "gpt-persisted"
	persistedStats.Record(context.Background(), coreusage.Record{
		APIKey:      persistedKey,
		Model:       persistedModel,
		Source:      "reload-persisted",
		AuthIndex:   "persisted-auth",
		RequestedAt: time.Date(2026, time.April, 2, 15, 0, 0, 0, time.UTC),
		Latency:     300 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  4,
			OutputTokens: 5,
			TotalTokens:  9,
		},
	})
	persistedManager := internalusage.NewPersistenceManager(&config.Config{UsageStatisticsPersistenceFile: newSnapshotPath}, configPath, persistedStats)
	if err := persistedManager.Save(); err != nil {
		t.Fatalf("Save() persisted snapshot error = %v, want nil", err)
	}

	started := make(chan struct{})
	var reloadFunc func(*config.Config)
	service := &Service{
		cfg:              baseCfg,
		configPath:       configPath,
		usagePersistence: internalusage.NewPersistenceManager(baseCfg, configPath, liveStats),
		watcherFactory: func(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
			reloadFunc = reload
			return &WatcherWrapper{}, nil
		},
		tokenProvider:  stubTokenClientProvider{},
		apiKeyProvider: stubAPIKeyClientProvider{},
		hooks: Hooks{OnAfterStart: func(*Service) {
			close(started)
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- service.Run(ctx)
	}()

	<-started
	deadline := time.Now().Add(2 * time.Second)
	for reloadFunc == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reloadFunc == nil {
		t.Fatal("reload callback = nil, want initialized watcher callback")
	}

	reloadCfg := *baseCfg
	reloadCfg.UsageStatisticsPersistenceFile = newSnapshotPath
	reloadFunc(&reloadCfg)
	cancel()

	if err := <-runErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}

	restoredStats := internalusage.NewRequestStatistics()
	snapshotManager := internalusage.NewPersistenceManager(&config.Config{UsageStatisticsPersistenceFile: newSnapshotPath}, configPath, restoredStats)
	if err := snapshotManager.Restore(); err != nil {
		t.Fatalf("Restore() reloaded snapshot error = %v, want nil", err)
	}

	snapshot := restoredStats.Snapshot()
	liveAPISnapshot, ok := snapshot.APIs[liveKey]
	if !ok {
		t.Fatal("reloaded snapshot missing live in-memory API key entry")
	}
	if modelSnapshot, ok := liveAPISnapshot.Models[liveModel]; !ok || modelSnapshot.TotalRequests == 0 {
		t.Fatal("reloaded snapshot missing live in-memory model entry")
	}

	persistedAPISnapshot, ok := snapshot.APIs[persistedKey]
	if !ok {
		t.Fatal("reloaded snapshot missing pre-existing persisted API key entry")
	}
	if modelSnapshot, ok := persistedAPISnapshot.Models[persistedModel]; !ok || modelSnapshot.TotalRequests == 0 {
		t.Fatal("reloaded snapshot missing pre-existing persisted model entry")
	}
}

func TestServiceReloadKeepsExistingUsagePersistenceWhenRelevantSettingsUnchanged(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	authDir := filepath.Join(tempDir, "auth")
	snapshotPath := filepath.Join(tempDir, "usage-statistics.json")

	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v, want nil", err)
	}
	if err := os.WriteFile(configPath, []byte("host: 127.0.0.1\nport: 0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want nil", err)
	}

	baseCfg := &config.Config{
		Host:                              "127.0.0.1",
		Port:                              0,
		AuthDir:                           authDir,
		UsageStatisticsEnabled:            true,
		UsageStatisticsPersistenceEnabled: true,
		UsageStatisticsPersistenceFile:    snapshotPath,
		CommercialMode:                    true,
	}

	originalStatisticsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	defer internalusage.SetStatisticsEnabled(originalStatisticsEnabled)

	originalSnapshot := internalusage.GetRequestStatistics().Snapshot()
	replaceUsageStatisticsSnapshot(internalusage.StatisticsSnapshot{})
	defer replaceUsageStatisticsSnapshot(originalSnapshot)

	started := make(chan struct{})
	var reloadFunc func(*config.Config)
	service := &Service{
		cfg:              baseCfg,
		configPath:       configPath,
		usagePersistence: internalusage.NewPersistenceManager(baseCfg, configPath, internalusage.GetRequestStatistics()),
		watcherFactory: func(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
			reloadFunc = reload
			return &WatcherWrapper{}, nil
		},
		tokenProvider:  stubTokenClientProvider{},
		apiKeyProvider: stubAPIKeyClientProvider{},
		hooks: Hooks{OnAfterStart: func(*Service) {
			close(started)
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- service.Run(ctx)
	}()

	<-started
	deadline := time.Now().Add(2 * time.Second)
	for reloadFunc == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reloadFunc == nil {
		t.Fatal("reload callback = nil, want initialized watcher callback")
	}

	originalManager := service.usagePersistence
	if originalManager == nil {
		t.Fatal("usagePersistence = nil, want initialized manager")
	}

	reloadCfg := *baseCfg
	reloadCfg.Routing.Strategy = "fill-first"
	reloadFunc(&reloadCfg)

	if service.usagePersistence != originalManager {
		t.Fatal("reload replaced usagePersistence despite unchanged persistence settings")
	}

	cancel()
	if err := <-runErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
}
