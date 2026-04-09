package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}

func TestSaveAndLoadSnapshotFile(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "persisted-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  11,
			OutputTokens: 22,
			TotalTokens:  33,
		},
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "usage-statistics.json")
	if err := SaveSnapshotFile(path, stats); err != nil {
		t.Fatalf("SaveSnapshotFile error: %v", err)
	}

	restored := NewRequestStatistics()
	result, err := LoadSnapshotFile(path, restored)
	if err != nil {
		t.Fatalf("LoadSnapshotFile error: %v", err)
	}
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("load merge result = %+v, want added=1 skipped=0", result)
	}

	snapshot := restored.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 33 {
		t.Fatalf("total tokens = %d, want 33", snapshot.TotalTokens)
	}
	details := snapshot.APIs["persisted-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat persisted file error: %v", err)
	}
}

func TestStartAutoPersistenceFlushesOnCancel(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "cancel-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 21, 9, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  5,
			OutputTokens: 7,
			TotalTokens:  12,
		},
	})

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	ctx, cancel := context.WithCancel(context.Background())
	StartAutoPersistence(ctx, path, time.Hour, stats)
	cancel()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("usage snapshot file was not flushed on cancel")
}
