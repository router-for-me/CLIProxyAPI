package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestServicePersistUsageStatisticsSavesDirtyDataWhenDisabled(t *testing.T) {
	stats := internalusage.NewRequestStatistics()
	recordUsageForServiceTest(t, stats, time.Date(2026, 3, 27, 9, 0, 0, 0, time.UTC))

	service := newUsagePersistenceTestService(t, stats, false, 30)
	service.persistUsageStatistics("shutdown")

	path := service.usageStatisticsFilePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persisted statistics file missing: %v", err)
	}
	if stats.HasPendingPersistence() {
		t.Fatalf("stats should be clean after shutdown persistence")
	}
}

func TestServiceApplyUsagePersistenceConfigChangePersistsOnDisable(t *testing.T) {
	stats := internalusage.NewRequestStatistics()
	recordUsageForServiceTest(t, stats, time.Date(2026, 3, 27, 10, 0, 0, 0, time.UTC))

	service := newUsagePersistenceTestService(t, stats, true, 30)
	newCfg := &sdkconfig.Config{
		UsageStatisticsEnabled:                false,
		UsageStatisticsPersistIntervalSeconds: 30,
	}

	service.cfgMu.Lock()
	service.cfg = newCfg
	service.cfgMu.Unlock()

	service.applyUsagePersistenceConfigChange(true, 30*time.Second, newCfg)

	path := service.usageStatisticsFilePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persisted statistics file missing after disable: %v", err)
	}
	if stats.HasPendingPersistence() {
		t.Fatalf("stats should be clean after disable-triggered persistence")
	}
}

func TestServiceApplyUsagePersistenceConfigChangeRestartsLoopOnIntervalChange(t *testing.T) {
	stats := internalusage.NewRequestStatistics()
	recordUsageForServiceTest(t, stats, time.Date(2026, 3, 27, 11, 0, 0, 0, time.UTC))

	service := newUsagePersistenceTestService(t, stats, true, 3)
	service.startUsagePersistenceLoop()
	defer service.stopUsagePersistenceLoop()

	time.Sleep(150 * time.Millisecond)

	newCfg := &sdkconfig.Config{
		UsageStatisticsEnabled:                true,
		UsageStatisticsPersistIntervalSeconds: 1,
	}

	service.cfgMu.Lock()
	service.cfg = newCfg
	service.cfgMu.Unlock()

	service.applyUsagePersistenceConfigChange(true, 3*time.Second, newCfg)

	waitForFile(t, service.usageStatisticsFilePath(), 2200*time.Millisecond)
	if stats.HasPendingPersistence() {
		t.Fatalf("stats should be clean after restarted persistence loop flushes")
	}
}

func newUsagePersistenceTestService(t *testing.T, stats *internalusage.RequestStatistics, enabled bool, intervalSeconds int) *Service {
	t.Helper()

	baseDir := t.TempDir()
	t.Setenv("WRITABLE_PATH", baseDir)

	return &Service{
		cfg: &sdkconfig.Config{
			UsageStatisticsEnabled:                enabled,
			UsageStatisticsPersistIntervalSeconds: intervalSeconds,
		},
		usageStats: stats,
	}
}

func recordUsageForServiceTest(t *testing.T, stats *internalusage.RequestStatistics, ts time.Time) {
	t.Helper()

	previousEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		internalusage.SetStatisticsEnabled(previousEnabled)
	})

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: ts,
		Latency:     500 * time.Millisecond,
		Source:      "service-test",
		AuthIndex:   "0",
		Detail: coreusage.Detail{
			InputTokens:  4,
			OutputTokens: 6,
			TotalTokens:  10,
		},
	})
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file %s was not created within %s: %v", filepath.Base(path), timeout, err)
	}
}
