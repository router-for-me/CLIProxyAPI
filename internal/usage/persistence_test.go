package usage

import (
	"context"
	"encoding/json"
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

func TestPersistenceLoadsLegacyUsageSnapshot(t *testing.T) {
	dir := t.TempDir()
	statsDir := filepath.Join(dir, "stats")
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := map[string]any{
		"version": 1,
		"usage": map[string]any{
			"total_requests": 7,
			"success_count":  5,
			"failure_count":  2,
			"total_tokens":   42,
			"apis": map[string]any{
				"legacy-api": map[string]any{
					"total_requests": 7,
					"total_tokens":   42,
					"models": map[string]any{
						"legacy-model": map[string]any{
							"total_requests": 7,
							"total_tokens":   42,
							"details":        []any{},
						},
					},
				},
			},
			"requests_by_day":  map[string]any{"2026-04-01": 7},
			"requests_by_hour": map[string]any{"10": 7},
			"tokens_by_day":    map[string]any{"2026-04-01": 42},
			"tokens_by_hour":   map[string]any{"10": 42},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(statsDir, legacyUsageFileName), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stats := NewRequestStatistics()
	persistence, err := StartPersistence(stats, statsDir, 14)
	if err != nil {
		t.Fatalf("StartPersistence() error = %v", err)
	}
	t.Cleanup(func() {
		_ = persistence.Stop()
	})

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 7 {
		t.Fatalf("TotalRequests = %d, want 7", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 42 {
		t.Fatalf("TotalTokens = %d, want 42", snapshot.TotalTokens)
	}
	apiSnapshot, ok := snapshot.APIs["legacy-api"]
	if !ok {
		t.Fatalf("expected legacy-api in APIs")
	}
	modelSnapshot, ok := apiSnapshot.Models["legacy-model"]
	if !ok {
		t.Fatalf("expected legacy-model in API models")
	}
	if modelSnapshot.TotalRequests != 7 {
		t.Fatalf("legacy model TotalRequests = %d, want 7", modelSnapshot.TotalRequests)
	}
}

func TestPersistenceOnlyRewritesDirtyDayFiles(t *testing.T) {
	dir := t.TempDir()
	statsDir := filepath.Join(dir, "stats")

	stats := NewRequestStatistics()
	persistence, err := StartPersistence(stats, statsDir, 14)
	if err != nil {
		t.Fatalf("StartPersistence() error = %v", err)
	}

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
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
		t.Fatalf("first Flush() error = %v", err)
	}

	day1Path := filepath.Join(statsDir, usageDailyDirectoryName, "2026-04-02.json")
	day2Path := filepath.Join(statsDir, usageDailyDirectoryName, "2026-04-03.json")
	day1InfoBefore, err := os.Stat(day1Path)
	if err != nil {
		t.Fatalf("Stat(day1 before) error = %v", err)
	}
	day2InfoBefore, err := os.Stat(day2Path)
	if err != nil {
		t.Fatalf("Stat(day2 before) error = %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  4,
			OutputTokens: 6,
			TotalTokens:  10,
		},
	})
	if err := persistence.Flush(); err != nil {
		t.Fatalf("second Flush() error = %v", err)
	}

	day1InfoAfter, err := os.Stat(day1Path)
	if err != nil {
		t.Fatalf("Stat(day1 after) error = %v", err)
	}
	day2InfoAfter, err := os.Stat(day2Path)
	if err != nil {
		t.Fatalf("Stat(day2 after) error = %v", err)
	}

	if !day1InfoAfter.ModTime().Equal(day1InfoBefore.ModTime()) {
		t.Fatalf("expected unchanged historical day file modtime, before=%v after=%v", day1InfoBefore.ModTime(), day1InfoAfter.ModTime())
	}
	if !day2InfoAfter.ModTime().After(day2InfoBefore.ModTime()) {
		t.Fatalf("expected dirty day file to be rewritten, before=%v after=%v", day2InfoBefore.ModTime(), day2InfoAfter.ModTime())
	}
}
