package forkruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func TestApplyUsageConfigNilNoops(t *testing.T) {
	restore := saveUsageRuntimeState()
	t.Cleanup(restore)

	usage.SetStatisticsEnabled(false)
	redisqueue.SetUsageStatisticsEnabled(true)
	redisqueue.SetRetentionSeconds(123)

	ApplyUsageConfig(nil)

	if usage.StatisticsEnabled() {
		t.Fatal("usage statistics changed for nil config")
	}
	if !redisqueue.UsageStatisticsEnabled() {
		t.Fatal("redisqueue usage statistics changed for nil config")
	}
	if got := redisqueue.RetentionSeconds(); got != 123 {
		t.Fatalf("redisqueue retention changed for nil config: got %d, want 123", got)
	}
}

func TestApplyUsageConfigAppliesUsageStatisticsAndRetention(t *testing.T) {
	restore := saveUsageRuntimeState()
	t.Cleanup(restore)

	usage.SetStatisticsEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	redisqueue.SetRetentionSeconds(60)

	ApplyUsageConfig(&config.Config{
		UsageStatisticsEnabled:          false,
		RedisUsageQueueRetentionSeconds: 456,
	})

	if usage.StatisticsEnabled() {
		t.Fatal("usage statistics enabled = true, want false")
	}
	if redisqueue.UsageStatisticsEnabled() {
		t.Fatal("redisqueue usage statistics enabled = true, want false")
	}
	if got := redisqueue.RetentionSeconds(); got != 456 {
		t.Fatalf("redisqueue retention = %d, want 456", got)
	}
}

func TestApplyUsageConfigEnablesUsageStatisticsAndSetsRetention(t *testing.T) {
	restore := saveUsageRuntimeState()
	t.Cleanup(restore)

	usage.SetStatisticsEnabled(false)
	redisqueue.SetUsageStatisticsEnabled(false)
	redisqueue.SetRetentionSeconds(60)

	ApplyUsageConfig(&config.Config{
		UsageStatisticsEnabled:          true,
		RedisUsageQueueRetentionSeconds: 789,
	})

	if !usage.StatisticsEnabled() {
		t.Fatal("usage statistics enabled = false, want true")
	}
	if !redisqueue.UsageStatisticsEnabled() {
		t.Fatal("redisqueue usage statistics enabled = false, want true")
	}
	if got := redisqueue.RetentionSeconds(); got != 789 {
		t.Fatalf("redisqueue retention = %d, want 789", got)
	}
}

func TestInitUsageStoreTrimsLogDirBeforeInitializingDefaultStore(t *testing.T) {
	restore := usage.SetDefaultStoreForTest(nil)
	defer restore()
	defer CloseUsageStore()

	parentDir := t.TempDir()
	trimmedLogDir := filepath.Join(parentDir, "usage-store")
	rawLogDir := filepath.Join(parentDir, " "+"usage-store")

	InitUsageStore("  "+trimmedLogDir+"  ", nil)

	if _, err := os.Stat(filepath.Join(trimmedLogDir, "usage.db")); err != nil {
		t.Fatalf("trimmed usage.db missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rawLogDir, "usage.db")); err == nil {
		t.Fatalf("raw usage.db unexpectedly created at %q", rawLogDir)
	}
}

func TestInitUsageStoreDoesNotCallSetterWhenInitFails(t *testing.T) {
	restore := usage.SetDefaultStoreForTest(nil)
	defer restore()

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	called := false
	InitUsageStore(filepath.Join(blocked, "logs"), usageStoreSetterFunc(func(usage.Store) {
		called = true
	}))

	if called {
		t.Fatal("setter was called despite init failure")
	}
}

type usageStoreSetterFunc func(usage.Store)

func (f usageStoreSetterFunc) SetUsageStore(store usage.Store) {
	f(store)
}

func saveUsageRuntimeState() func() {
	usageStatisticsEnabled := usage.StatisticsEnabled()
	redisUsageStatisticsEnabled := redisqueue.UsageStatisticsEnabled()
	redisRetentionSeconds := redisqueue.RetentionSeconds()

	return func() {
		usage.SetStatisticsEnabled(usageStatisticsEnabled)
		redisqueue.SetUsageStatisticsEnabled(redisUsageStatisticsEnabled)
		redisqueue.SetRetentionSeconds(redisRetentionSeconds)
	}
}
