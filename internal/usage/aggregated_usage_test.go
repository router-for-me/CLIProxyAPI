package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestAggregatedUsageSnapshotDoesNotMergeImportedRollingWindows(t *testing.T) {
	stats := NewRequestStatistics()
	stats.MergeImportedAggregatedSnapshot(AggregatedUsageSnapshot{
		GeneratedAt: time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC),
		ModelNames:  []string{"gpt-5.4"},
		Windows: map[string]AggregatedUsageWindow{
			"1h": {
				TotalRequests: 3,
				TotalTokens:   30,
				ModelNames:    []string{"gpt-5.4"},
			},
			"all": {
				TotalRequests: 3,
				TotalTokens:   30,
				ModelNames:    []string{"gpt-5.4"},
			},
		},
	})

	snapshot := stats.AggregatedUsageSnapshot(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))

	if got := snapshot.Windows["1h"].TotalRequests; got != 0 {
		t.Fatalf("1h total_requests = %d, want 0", got)
	}
	if got := snapshot.Windows["1h"].TotalTokens; got != 0 {
		t.Fatalf("1h total_tokens = %d, want 0", got)
	}
	if got := snapshot.Windows["all"].TotalRequests; got != 3 {
		t.Fatalf("all total_requests = %d, want 3", got)
	}
	if got := snapshot.Windows["all"].TotalTokens; got != 30 {
		t.Fatalf("all total_tokens = %d, want 30", got)
	}
}

func TestAggregatedUsageSnapshotSkipsDuplicateImportedAllWindow(t *testing.T) {
	stats := NewRequestStatistics()
	imported := AggregatedUsageSnapshot{
		GeneratedAt: time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC),
		ModelNames:  []string{"gpt-5.4"},
		Windows: map[string]AggregatedUsageWindow{
			"all": {
				TotalRequests: 2,
				TotalTokens:   20,
				ModelNames:    []string{"gpt-5.4"},
			},
		},
	}

	stats.MergeImportedAggregatedSnapshot(imported)
	stats.MergeImportedAggregatedSnapshot(imported)

	snapshot := stats.AggregatedUsageSnapshot(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if got := snapshot.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("all total_requests = %d, want 2", got)
	}
	if got := snapshot.Windows["all"].TotalTokens; got != 20 {
		t.Fatalf("all total_tokens = %d, want 20", got)
	}
}

func TestAggregatedUsageSnapshotIgnoresDetailRetentionLimit(t *testing.T) {
	previousLimit := DetailRetentionLimit()
	SetDetailRetentionLimit(3)
	t.Cleanup(func() { SetDetailRetentionLimit(previousLimit) })

	stats := NewRequestStatistics()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "retention-test",
			Model:       "gpt-5.4",
			RequestedAt: now.Add(time.Duration(-55+i) * time.Minute),
			Detail: coreusage.Detail{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		})
	}

	details := stats.Snapshot().APIs["retention-test"].Models["gpt-5.4"].Details
	if got := len(details); got != 3 {
		t.Fatalf("retained details len = %d, want 3", got)
	}

	snapshot := stats.AggregatedUsageSnapshot(now)
	if got := snapshot.Windows["1h"].TotalRequests; got != 6 {
		t.Fatalf("1h total_requests = %d, want 6", got)
	}
	if got := snapshot.Windows["1h"].TotalTokens; got != 12 {
		t.Fatalf("1h total_tokens = %d, want 12", got)
	}
}

func TestAggregatedUsageSnapshotRollsUpExpiredAggregateRecords(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-8 * 24 * time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-30 * time.Minute),
		Detail: coreusage.Detail{
			InputTokens:  2,
			OutputTokens: 2,
			TotalTokens:  4,
		},
	})

	if got := len(stats.aggregateRecords); got != 1 {
		t.Fatalf("aggregateRecords len = %d, want 1", got)
	}

	snapshot := stats.AggregatedUsageSnapshot(now)
	if got := snapshot.Windows["1h"].TotalRequests; got != 1 {
		t.Fatalf("1h total_requests = %d, want 1", got)
	}
	if got := snapshot.Windows["7d"].TotalRequests; got != 1 {
		t.Fatalf("7d total_requests = %d, want 1", got)
	}
	if got := snapshot.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("all total_requests = %d, want 2", got)
	}
	if got := snapshot.Windows["all"].TotalTokens; got != 6 {
		t.Fatalf("all total_tokens = %d, want 6", got)
	}
}

func TestAggregatedUsageSnapshotRollsUpOutOfOrderExpiredRecord(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: now,
		Detail: coreusage.Detail{
			InputTokens:  2,
			OutputTokens: 2,
			TotalTokens:  4,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-8 * 24 * time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		},
	})

	if got := len(stats.aggregateRecords); got != 1 {
		t.Fatalf("aggregateRecords len = %d, want 1", got)
	}

	snapshot := stats.AggregatedUsageSnapshot(now)
	if got := snapshot.Windows["7d"].TotalRequests; got != 1 {
		t.Fatalf("7d total_requests = %d, want 1", got)
	}
	if got := snapshot.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("all total_requests = %d, want 2", got)
	}
}

func TestAggregatedUsageSnapshotPrunesDeferredOutOfOrderExpiredRecord(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: now,
		Detail: coreusage.Detail{
			InputTokens:  2,
			OutputTokens: 2,
			TotalTokens:  4,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-6 * 24 * time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
			TotalTokens:  2,
		},
	})
	if got := len(stats.aggregateRecords); got != 2 {
		t.Fatalf("aggregateRecords len after second record = %d, want 2", got)
	}

	later := now.Add(48 * time.Hour)
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "rollup-test",
		Model:       "gpt-5.4",
		RequestedAt: later,
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 3,
			TotalTokens:  6,
		},
	})

	if got := len(stats.aggregateRecords); got != 2 {
		t.Fatalf("aggregateRecords len after pruning = %d, want 2", got)
	}

	snapshot := stats.AggregatedUsageSnapshot(later)
	if got := snapshot.Windows["7d"].TotalRequests; got != 2 {
		t.Fatalf("7d total_requests = %d, want 2", got)
	}
	if got := snapshot.Windows["all"].TotalRequests; got != 3 {
		t.Fatalf("all total_requests = %d, want 3", got)
	}
	if got := snapshot.Windows["all"].TotalTokens; got != 12 {
		t.Fatalf("all total_tokens = %d, want 12", got)
	}
}
