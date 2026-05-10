package forkruntime

import (
	"context"
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
	restoreRuntime := saveUsageRuntimeState()
	defer restoreRuntime()
	usage.SetStatisticsEnabled(true)
	restoreStore := usage.SetDefaultStoreForTest(nil)
	defer restoreStore()
	defer CloseUsageStore()

	parentDir := t.TempDir()
	trimmedLogDir := filepath.Join(parentDir, "usage-store")
	rawLogDir := filepath.Join(parentDir, " "+"usage-store")

	InitUsageStore("  " + trimmedLogDir + "  ")

	if usage.DefaultStore() == nil {
		t.Fatal("DefaultStore() = nil, want initialized store")
	}
	if _, err := os.Stat(filepath.Join(trimmedLogDir, "usage.db")); err != nil {
		t.Fatalf("trimmed usage.db missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rawLogDir, "usage.db")); err == nil {
		t.Fatalf("raw usage.db unexpectedly created at %q", rawLogDir)
	}
}

func TestCloseUsageStoreClosesDefaultStore(t *testing.T) {
	store := &closeRecordingStore{}
	restore := usage.SetDefaultStoreForTest(store)
	defer restore()

	CloseUsageStore()

	if !store.closed {
		t.Fatal("default store was not closed")
	}
	if got := usage.DefaultStore(); got != nil {
		t.Fatalf("DefaultStore() = %T, want nil", got)
	}
}

type closeRecordingStore struct {
	closed bool
}

func (s *closeRecordingStore) Insert(ctx context.Context, record usage.Record) error { return nil }

func (s *closeRecordingStore) Query(ctx context.Context, rng usage.QueryRange) (usage.APIUsage, error) {
	return nil, nil
}

func (s *closeRecordingStore) Delete(ctx context.Context, ids []string) (usage.DeleteResult, error) {
	return usage.DeleteResult{}, nil
}

func (s *closeRecordingStore) Close() error {
	s.closed = true
	return nil
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
