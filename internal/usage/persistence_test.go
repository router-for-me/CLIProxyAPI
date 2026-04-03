package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestResolvePersistencePathPrefersExplicitConfigFile(t *testing.T) {
	t.Setenv("WRITABLE_PATH", filepath.Join("/tmp", "writable"))
	cfg := &internalconfig.Config{UsageStatisticsPersistenceFile: "custom/usage.json"}

	got := resolvePersistencePath(cfg, filepath.Join("/tmp", "config", "config.yaml"))
	want := filepath.Clean("custom/usage.json")
	if got != want {
		t.Fatalf("resolvePersistencePath() = %q, want %q", got, want)
	}
}

func TestResolvePersistencePathPrefersWritablePath(t *testing.T) {
	t.Setenv("WRITABLE_PATH", filepath.Join("/tmp", "writable"))
	cfg := &internalconfig.Config{}
	configFile := filepath.Join("/tmp", "config", "config.yaml")

	got := resolvePersistencePath(cfg, configFile)
	want := filepath.Join(filepath.Join("/tmp", "writable"), "data", persistenceFileName)
	if got != want {
		t.Fatalf("resolvePersistencePath() = %q, want %q", got, want)
	}
}

func TestResolvePersistencePathUsesConfigDirectoryFallback(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	cfg := &internalconfig.Config{}
	configFile := filepath.Join("/tmp", "config", "config.yaml")

	got := resolvePersistencePath(cfg, configFile)
	want := filepath.Join(filepath.Dir(configFile), "data", persistenceFileName)
	if got != want {
		t.Fatalf("resolvePersistencePath() = %q, want %q", got, want)
	}
}

func TestLoadSnapshotReturnsEmptyForMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)

	snapshot, ok, err := loadSnapshotFile(path)
	if err != nil {
		t.Fatalf("loadSnapshotFile() error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("loadSnapshotFile() ok = true, want false")
	}
	if snapshot.Version != 0 || !snapshot.ExportedAt.IsZero() || !snapshotsEqual(snapshot.Usage, StatisticsSnapshot{}) {
		t.Fatalf("loadSnapshotFile() snapshot = %+v, want zero value", snapshot)
	}
}

func TestSaveAndLoadSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", persistenceFileName)
	want := testSnapshot(
		time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		"user@example.com",
		"0",
		TokenStats{InputTokens: 20, OutputTokens: 22, TotalTokens: 42},
		false,
	)

	if err := saveSnapshotFile(path, want); err != nil {
		t.Fatalf("saveSnapshotFile() error = %v, want nil", err)
	}

	assertSnapshotFileEquals(t, path, want)
	assertSnapshotFilePermissions(t, path, 0o600)
	assertSnapshotParentDirPermissions(t, path, 0o700)
}

func TestSaveSnapshotFileOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", persistenceFileName)
	first := testSnapshot(
		time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		"first@example.com",
		"0",
		TokenStats{InputTokens: 20, OutputTokens: 22, TotalTokens: 42},
		false,
	)
	second := testSnapshot(
		time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC),
		"second@example.com",
		"1",
		TokenStats{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		true,
	)

	if err := saveSnapshotFile(path, first); err != nil {
		t.Fatalf("first saveSnapshotFile() error = %v, want nil", err)
	}
	if err := saveSnapshotFile(path, second); err != nil {
		t.Fatalf("second saveSnapshotFile() error = %v, want nil", err)
	}

	assertSnapshotFileEquals(t, path, second)
}

func TestSaveSnapshotFileRestoresPreviousSnapshotWhenReplacementFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", persistenceFileName)
	first := testSnapshot(
		time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		"first@example.com",
		"0",
		TokenStats{InputTokens: 20, OutputTokens: 22, TotalTokens: 42},
		false,
	)
	second := testSnapshot(
		time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC),
		"second@example.com",
		"1",
		TokenStats{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		true,
	)

	if err := saveSnapshotFile(path, first); err != nil {
		t.Fatalf("first saveSnapshotFile() error = %v, want nil", err)
	}

	originalRename := renameFile
	defer func() { renameFile = originalRename }()

	var replacementFailed bool
	backup := backupPath(path)
	renameFile = func(oldPath, newPath string) error {
		if !replacementFailed && oldPath != backup && newPath == path {
			replacementFailed = true
			return errors.New("injected rename failure")
		}
		return os.Rename(oldPath, newPath)
	}

	err := saveSnapshotFile(path, second)
	if err == nil {
		t.Fatal("saveSnapshotFile() error = nil, want non-nil")
	}
	if !replacementFailed {
		t.Fatal("replacement rename was not exercised")
	}
	if !strings.Contains(err.Error(), "replace usage snapshot") {
		t.Fatalf("saveSnapshotFile() error = %q, want replace context", err)
	}

	assertSnapshotFileEquals(t, path, first)
	if _, statErr := os.Stat(backup); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup file stat error = %v, want not exists", statErr)
	}
}

func TestSaveSnapshotFileIgnoresBackupCleanupFailureAfterSuccessfulReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", persistenceFileName)
	first := testSnapshot(
		time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		"first@example.com",
		"0",
		TokenStats{InputTokens: 20, OutputTokens: 22, TotalTokens: 42},
		false,
	)
	second := testSnapshot(
		time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC),
		"second@example.com",
		"1",
		TokenStats{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		true,
	)

	if err := saveSnapshotFile(path, first); err != nil {
		t.Fatalf("first saveSnapshotFile() error = %v, want nil", err)
	}

	originalRemove := removeFile
	defer func() { removeFile = originalRemove }()

	backup := backupPath(path)
	var cleanupAttempted bool
	removeFile = func(name string) error {
		if name == backup {
			if _, err := os.Stat(name); err == nil {
				cleanupAttempted = true
				return errors.New("injected cleanup failure")
			}
		}
		return os.Remove(name)
	}

	if err := saveSnapshotFile(path, second); err != nil {
		t.Fatalf("saveSnapshotFile() error = %v, want nil", err)
	}
	if !cleanupAttempted {
		t.Fatal("backup cleanup was not exercised")
	}

	assertSnapshotFileEquals(t, path, second)
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup file stat error = %v, want backup to remain after cleanup failure", err)
	}
}

func TestPersistenceManagerStartIsIdempotentAndStopAndSavePersistsLatestSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	stats := NewRequestStatistics()
	manager := &PersistenceManager{stats: stats, path: path}

	manager.Start(nil)
	manager.Start(nil)

	latest := testSnapshot(
		time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		"latest@example.com",
		"2",
		TokenStats{InputTokens: 30, OutputTokens: 12, TotalTokens: 42},
		false,
	)
	stats.MergeSnapshot(latest)

	if err := manager.StopAndSave(); err != nil {
		t.Fatalf("StopAndSave() error = %v, want nil", err)
	}

	assertSnapshotFileEquals(t, path, latest)
}

func TestPersistenceManagerSaveSkipsRewriteWhenSnapshotUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	stats := NewRequestStatistics()
	manager := &PersistenceManager{stats: stats, path: path}

	snapshot := testSnapshot(
		time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		"stable@example.com",
		"2",
		TokenStats{InputTokens: 30, OutputTokens: 12, TotalTokens: 42},
		false,
	)
	stats.MergeSnapshot(snapshot)

	if err := manager.Save(); err != nil {
		t.Fatalf("first Save() error = %v, want nil", err)
	}
	firstData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() after first Save error = %v, want nil", err)
	}

	if err := manager.Save(); err != nil {
		t.Fatalf("second Save() error = %v, want nil", err)
	}
	secondData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() after second Save error = %v, want nil", err)
	}

	if string(secondData) != string(firstData) {
		t.Fatal("Save() rewrote snapshot despite no usage changes")
	}
}

func TestPersistenceManagerRestoreMarksManagerDirtyAndMergesSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	stats := NewRequestStatistics()
	existingTimestamp := time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"api-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: existingTimestamp,
							Source:    "existing",
							AuthIndex: "0",
							Tokens:    TokenStats{InputTokens: 10, TotalTokens: 10},
						}},
					},
				},
			},
		},
	})

	importTimestamp := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	importSnapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"api-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{
							{
								Timestamp: existingTimestamp,
								Source:    "existing",
								AuthIndex: "0",
								Tokens:    TokenStats{InputTokens: 10, TotalTokens: 10},
							},
							{
								Timestamp: importTimestamp,
								Source:    "imported",
								AuthIndex: "1",
								Tokens:    TokenStats{InputTokens: 20, TotalTokens: 20},
							},
						},
					},
				},
			},
		},
	}
	if err := saveSnapshotFile(path, importSnapshot); err != nil {
		t.Fatalf("saveSnapshotFile() error = %v, want nil", err)
	}

	manager := &PersistenceManager{stats: stats, path: path}
	if err := manager.Restore(); err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}

	got := stats.Snapshot()
	details := got.APIs["api-key"].Models["gpt-5.4"].Details
	if len(details) != 2 {
		t.Fatalf("details len = %d, want 2", len(details))
	}
	if got.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2", got.TotalRequests)
	}
	if got.TotalTokens != 30 {
		t.Fatalf("TotalTokens = %d, want 30", got.TotalTokens)
	}

	if err := manager.Save(); err != nil {
		t.Fatalf("Save() after Restore error = %v, want nil", err)
	}
	assertSnapshotFileEquals(t, path, got)
}

func TestPersistenceManagerRestorePreservesFallbackProviderIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	stats := NewRequestStatistics()
	fallbackIdentifier := "gemini"
	importSnapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			fallbackIdentifier: {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
							Source:    "imported",
							AuthIndex: "1",
							Tokens:    TokenStats{InputTokens: 20, TotalTokens: 20},
						}},
					},
				},
			},
		},
	}
	if err := saveSnapshotFile(path, importSnapshot); err != nil {
		t.Fatalf("saveSnapshotFile() error = %v, want nil", err)
	}

	manager := &PersistenceManager{stats: stats, path: path}
	if err := manager.Restore(); err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}

	got := stats.Snapshot()
	if _, ok := got.APIs[fallbackIdentifier]; !ok {
		t.Fatalf("Restore() changed fallback identifier %q", fallbackIdentifier)
	}
}

func TestPersistenceManagerRestoreFallsBackToBackupWhenPrimaryMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	backup := backupPath(path)
	want := testSnapshot(
		time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		"imported",
		"1",
		TokenStats{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
		false,
	)
	if err := saveSnapshotFile(backup, want); err != nil {
		t.Fatalf("saveSnapshotFile() backup error = %v, want nil", err)
	}

	stats := NewRequestStatistics()
	manager := &PersistenceManager{stats: stats, path: path}
	if err := manager.Restore(); err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}

	got := stats.Snapshot()
	if !snapshotsEqual(got, want) {
		encodedGot, _ := json.Marshal(got)
		encodedWant, _ := json.Marshal(want)
		t.Fatalf("restored snapshot = %s, want %s", encodedGot, encodedWant)
	}
}

func TestPersistenceManagerRestoreFallsBackToBackupWhenPrimaryIsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	backup := backupPath(path)
	want := testSnapshot(
		time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		"imported",
		"1",
		TokenStats{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
		false,
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v, want nil", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() primary error = %v, want nil", err)
	}
	if err := saveSnapshotFile(backup, want); err != nil {
		t.Fatalf("saveSnapshotFile() backup error = %v, want nil", err)
	}

	stats := NewRequestStatistics()
	manager := &PersistenceManager{stats: stats, path: path}
	if err := manager.Restore(); err != nil {
		t.Fatalf("Restore() error = %v, want nil", err)
	}

	got := stats.Snapshot()
	if !snapshotsEqual(got, want) {
		encodedGot, _ := json.Marshal(got)
		encodedWant, _ := json.Marshal(want)
		t.Fatalf("restored snapshot = %s, want %s", encodedGot, encodedWant)
	}
}

func TestPersistenceManagerRestoreReturnsErrorOnBadSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), persistenceFileName)
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v, want nil", err)
	}

	manager := &PersistenceManager{stats: NewRequestStatistics(), path: path}
	if err := manager.Restore(); err == nil {
		t.Fatal("Restore() error = nil, want non-nil")
	}
}

func TestEnabledForConfigRequiresUsageCollection(t *testing.T) {
	if EnabledForConfig(nil) {
		t.Fatal("EnabledForConfig(nil) = true, want false")
	}
	if EnabledForConfig(&internalconfig.Config{UsageStatisticsEnabled: false, UsageStatisticsPersistenceEnabled: true}) {
		t.Fatal("EnabledForConfig() = true when collection disabled, want false")
	}
	if EnabledForConfig(&internalconfig.Config{UsageStatisticsEnabled: true, UsageStatisticsPersistenceEnabled: false}) {
		t.Fatal("EnabledForConfig() = true when persistence disabled, want false")
	}
	if !EnabledForConfig(&internalconfig.Config{UsageStatisticsEnabled: true, UsageStatisticsPersistenceEnabled: true}) {
		t.Fatal("EnabledForConfig() = false, want true")
	}
}

func assertSnapshotFileEquals(t *testing.T, path string, want StatisticsSnapshot) {
	t.Helper()

	got, ok, err := loadSnapshotFile(path)
	if err != nil {
		t.Fatalf("loadSnapshotFile() error = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("loadSnapshotFile() ok = false, want true")
	}
	if got.Version != persistenceVersion {
		t.Fatalf("Version = %d, want %d", got.Version, persistenceVersion)
	}
	if got.ExportedAt.IsZero() {
		t.Fatal("ExportedAt = zero, want non-zero timestamp")
	}
	if !snapshotsEqual(got.Usage, want) {
		encodedGot, _ := json.Marshal(got.Usage)
		encodedWant, _ := json.Marshal(want)
		t.Fatalf("Usage = %s, want %s", encodedGot, encodedWant)
	}
}

func assertSnapshotFilePermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("file mode enforcement is not supported on Windows")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v, want nil", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("file permissions = %03o, want %03o", got, want)
	}
}

func assertSnapshotParentDirPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("directory mode enforcement is not supported on Windows")
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat() parent dir error = %v, want nil", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("parent dir permissions = %03o, want %03o", got, want)
	}
}

func testSnapshot(timestamp time.Time, source, authIndex string, tokens TokenStats, failed bool) StatisticsSnapshot {
	return StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  boolToCount(!failed),
		FailureCount:  boolToCount(failed),
		TotalTokens:   tokens.TotalTokens,
		APIs: map[string]APISnapshot{
			"api-key": {
				TotalRequests: 1,
				TotalTokens:   tokens.TotalTokens,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 1,
						TotalTokens:   tokens.TotalTokens,
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 500,
							Source:    source,
							AuthIndex: authIndex,
							Tokens:    tokens,
							Failed:    failed,
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{timestamp.Format("2006-01-02"): 1},
		RequestsByHour: map[string]int64{timestamp.Format("15"): 1},
		TokensByDay:    map[string]int64{timestamp.Format("2006-01-02"): tokens.TotalTokens},
		TokensByHour:   map[string]int64{timestamp.Format("15"): tokens.TotalTokens},
	}
}

func boolToCount(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

