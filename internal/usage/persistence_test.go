package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestPersistenceFlushAndRestore(t *testing.T) {
	dir := t.TempDir()

	original := NewRequestStatistics()
	persistence, err := StartPersistence(original, filepath.Join(dir, "stats"), 14)
	if err != nil {
		t.Fatalf("StartPersistence() error = %v", err)
	}

	original.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})
	original.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  20,
		},
	})
	if err := persistence.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "stats", usageSummaryFileName)); err != nil {
		t.Fatalf("summary file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stats", usageDailyDirectoryName, "2026-04-02.json")); err != nil {
		t.Fatalf("day file 2026-04-02 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stats", usageDailyDirectoryName, "2026-04-03.json")); err != nil {
		t.Fatalf("day file 2026-04-03 missing: %v", err)
	}

	restored := NewRequestStatistics()
	restoredPersistence, err := StartPersistence(restored, filepath.Join(dir, "stats"), 14)
	if err != nil {
		t.Fatalf("StartPersistence() restore error = %v", err)
	}
	t.Cleanup(func() {
		_ = restoredPersistence.Stop()
	})

	snapshot := restored.Snapshot()
	if snapshot.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2", snapshot.TotalRequests)
	}
	apiSnapshot, ok := snapshot.APIs["test-api"]
	if !ok {
		t.Fatalf("expected API snapshot for test-api")
	}
	modelSnapshot, ok := apiSnapshot.Models["gpt-test"]
	if !ok {
		t.Fatalf("expected model snapshot for gpt-test")
	}
	if modelSnapshot.TotalTokens != 35 {
		t.Fatalf("TotalTokens = %d, want 35", modelSnapshot.TotalTokens)
	}
	if len(modelSnapshot.Details) != 2 {
		t.Fatalf("Details len = %d, want 2", len(modelSnapshot.Details))
	}
}

func TestPersistencePrunesExpiredDailyDetails(t *testing.T) {
	dir := t.TempDir()

	stats := NewRequestStatistics()
	persistence, err := StartPersistence(stats, filepath.Join(dir, "stats"), 2)
	if err != nil {
		t.Fatalf("StartPersistence() error = %v", err)
	}

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Now().UTC().AddDate(0, 0, -5),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 2,
			TotalTokens:  12,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Now().UTC(),
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
	})
	if err := persistence.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	snapshot := stats.Snapshot()
	modelSnapshot := snapshot.APIs["test-api"].Models["gpt-test"]
	if snapshot.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2", snapshot.TotalRequests)
	}
	if len(modelSnapshot.Details) != 1 {
		t.Fatalf("Details len = %d, want 1 after prune", len(modelSnapshot.Details))
	}
	if modelSnapshot.TotalTokens != 19 {
		t.Fatalf("TotalTokens = %d, want 19 aggregate preserved", modelSnapshot.TotalTokens)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "stats", usageDailyDirectoryName))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("daily file count = %d, want 1", len(entries))
	}
}
