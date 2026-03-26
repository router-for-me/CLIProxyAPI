package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestPersistAndRestoreRequestStatisticsRoundTrip(t *testing.T) {
	stats := NewRequestStatistics()
	recordUsageForTest(stats, coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Source:      "user@example.com",
		AuthIndex:   "0",
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	recordUsageForTest(stats, coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC),
		Latency:     900 * time.Millisecond,
		Source:      "user@example.com",
		AuthIndex:   "0",
		Failed:      true,
		Detail: coreusage.Detail{
			InputTokens:  5,
			OutputTokens: 7,
			TotalTokens:  12,
		},
	})

	path := filepath.Join(t.TempDir(), "logs", StatisticsFileName)
	saved, err := PersistRequestStatistics(path, stats)
	if err != nil {
		t.Fatalf("PersistRequestStatistics() error = %v", err)
	}
	if !saved {
		t.Fatalf("PersistRequestStatistics() saved = false, want true")
	}
	if stats.HasPendingPersistence() {
		t.Fatalf("stats should be clean after persistence")
	}
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("persisted file missing: %v", errStat)
	}

	restored := NewRequestStatistics()
	loaded, result, err := RestoreRequestStatistics(path, restored)
	if err != nil {
		t.Fatalf("RestoreRequestStatistics() error = %v", err)
	}
	if !loaded {
		t.Fatalf("RestoreRequestStatistics() loaded = false, want true")
	}
	if result.Added != 2 || result.Skipped != 0 {
		t.Fatalf("RestoreRequestStatistics() result = %+v, want added=2 skipped=0", result)
	}

	snapshot := restored.Snapshot()
	if snapshot.TotalRequests != 2 {
		t.Fatalf("snapshot.TotalRequests = %d, want 2", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 1 {
		t.Fatalf("snapshot.SuccessCount = %d, want 1", snapshot.SuccessCount)
	}
	if snapshot.FailureCount != 1 {
		t.Fatalf("snapshot.FailureCount = %d, want 1", snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 42 {
		t.Fatalf("snapshot.TotalTokens = %d, want 42", snapshot.TotalTokens)
	}
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 2 {
		t.Fatalf("details len = %d, want 2", len(details))
	}
	if restored.HasPendingPersistence() {
		t.Fatalf("restored stats should be clean immediately after restore")
	}

	recordUsageForTest(restored, coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC),
		Latency:     300 * time.Millisecond,
		Source:      "user@example.com",
		AuthIndex:   "1",
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
	})
	saved, err = PersistRequestStatistics(path, restored)
	if err != nil {
		t.Fatalf("PersistRequestStatistics() after restore error = %v", err)
	}
	if !saved {
		t.Fatalf("PersistRequestStatistics() after restore saved = false, want true")
	}

	reloaded := NewRequestStatistics()
	loaded, result, err = RestoreRequestStatistics(path, reloaded)
	if err != nil {
		t.Fatalf("RestoreRequestStatistics() second restore error = %v", err)
	}
	if !loaded {
		t.Fatalf("RestoreRequestStatistics() second restore loaded = false, want true")
	}
	if result.Added != 3 || result.Skipped != 0 {
		t.Fatalf("RestoreRequestStatistics() second restore result = %+v, want added=3 skipped=0", result)
	}
	if got := reloaded.Snapshot().TotalRequests; got != 3 {
		t.Fatalf("reloaded snapshot.TotalRequests = %d, want 3", got)
	}
}

func TestRestoreRequestStatisticsMissingFileNoop(t *testing.T) {
	stats := NewRequestStatistics()
	path := filepath.Join(t.TempDir(), "logs", StatisticsFileName)

	loaded, result, err := RestoreRequestStatistics(path, stats)
	if err != nil {
		t.Fatalf("RestoreRequestStatistics() error = %v", err)
	}
	if loaded {
		t.Fatalf("RestoreRequestStatistics() loaded = true, want false")
	}
	if result.Added != 0 || result.Skipped != 0 {
		t.Fatalf("RestoreRequestStatistics() result = %+v, want zero", result)
	}
}

func TestRestoreRequestStatisticsInvalidFileReturnsError(t *testing.T) {
	stats := NewRequestStatistics()
	path := filepath.Join(t.TempDir(), StatisticsFileName)
	if err := os.WriteFile(path, []byte("{invalid"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	loaded, _, err := RestoreRequestStatistics(path, stats)
	if err == nil {
		t.Fatalf("RestoreRequestStatistics() error = nil, want non-nil")
	}
	if loaded {
		t.Fatalf("RestoreRequestStatistics() loaded = true, want false")
	}
	if got := stats.Snapshot().TotalRequests; got != 0 {
		t.Fatalf("stats changed after invalid restore, total_requests = %d", got)
	}
}

func TestRestoreRequestStatisticsDeduplicatesRepeatedLoads(t *testing.T) {
	stats := NewRequestStatistics()
	recordUsageForTest(stats, coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 26, 10, 0, 0, 0, time.UTC),
		Source:      "user@example.com",
		AuthIndex:   "0",
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	path := filepath.Join(t.TempDir(), StatisticsFileName)
	if _, err := PersistRequestStatistics(path, stats); err != nil {
		t.Fatalf("PersistRequestStatistics() error = %v", err)
	}

	restored := NewRequestStatistics()
	loaded, result, err := RestoreRequestStatistics(path, restored)
	if err != nil {
		t.Fatalf("first RestoreRequestStatistics() error = %v", err)
	}
	if !loaded || result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first RestoreRequestStatistics() = loaded=%t result=%+v", loaded, result)
	}

	loaded, result, err = RestoreRequestStatistics(path, restored)
	if err != nil {
		t.Fatalf("second RestoreRequestStatistics() error = %v", err)
	}
	if !loaded || result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second RestoreRequestStatistics() = loaded=%t result=%+v", loaded, result)
	}
	if got := restored.Snapshot().TotalRequests; got != 1 {
		t.Fatalf("restored snapshot.TotalRequests = %d, want 1", got)
	}
}

func recordUsageForTest(stats *RequestStatistics, record coreusage.Record) {
	stats.Record(context.Background(), record)
}
