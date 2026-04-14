package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsSnapshotSummaryAndResetClearsMemory(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "api-1",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})

	snapshot := stats.SnapshotSummaryAndReset()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("summary total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 15 {
		t.Fatalf("summary total tokens = %d, want 15", snapshot.TotalTokens)
	}
	if got := stats.Snapshot(); got.TotalRequests != 0 || got.TotalTokens != 0 {
		t.Fatalf("stats after reset = %+v, want empty snapshot", got)
	}
}

func TestStorePersistedUsageSnapshotMergesSummaries(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/usage.json"

	first := NewRequestStatistics()
	first.Record(context.Background(), coreusage.Record{
		APIKey:      "api-1",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})
	if _, err := storePersistedUsageSnapshot(path, first.SnapshotSummary()); err != nil {
		t.Fatalf("store first snapshot: %v", err)
	}

	second := NewRequestStatistics()
	second.Record(context.Background(), coreusage.Record{
		APIKey:      "api-1",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 14, 11, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  2,
			OutputTokens: 3,
			TotalTokens:  5,
		},
	})
	merged, err := storePersistedUsageSnapshot(path, second.SnapshotSummary())
	if err != nil {
		t.Fatalf("store second snapshot: %v", err)
	}
	if merged.TotalRequests != 2 {
		t.Fatalf("merged total requests = %d, want 2", merged.TotalRequests)
	}
	if merged.TotalTokens != 20 {
		t.Fatalf("merged total tokens = %d, want 20", merged.TotalTokens)
	}
	model := merged.APIs["api-1"].Models["gpt-5.4"]
	if len(model.Details) != 0 {
		t.Fatalf("persisted details len = %d, want 0", len(model.Details))
	}
}

func TestSnapshotWithPersistenceMergesPersistedSummaryAndMemory(t *testing.T) {
	previousStats := defaultRequestStatistics
	t.Cleanup(func() {
		defaultPersistence = persistenceController{}
		defaultRequestStatistics = previousStats
	})
	defaultRequestStatistics = NewRequestStatistics()

	defaultPersistence = persistenceController{
		cfg: PersistenceConfig{Enabled: true, FilePath: "memory"},
		persisted: StatisticsSnapshot{
			TotalRequests: 2,
			SuccessCount:  2,
			TotalTokens:   30,
			APIs: map[string]APISnapshot{
				"api-1": {
					TotalRequests: 2,
					TotalTokens:   30,
					Models: map[string]ModelSnapshot{
						"gpt-5.4": {TotalRequests: 2, TotalTokens: 30},
					},
				},
			},
		},
	}

	defaultRequestStatistics.Record(context.Background(), coreusage.Record{
		APIKey:      "api-1",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  4,
			OutputTokens: 6,
			TotalTokens:  10,
		},
	})

	snapshot := SnapshotWithPersistence(defaultRequestStatistics, true)
	if snapshot.TotalRequests != 3 {
		t.Fatalf("total requests = %d, want 3", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 40 {
		t.Fatalf("total tokens = %d, want 40", snapshot.TotalTokens)
	}
	model := snapshot.APIs["api-1"].Models["gpt-5.4"]
	if len(model.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(model.Details))
	}
}
