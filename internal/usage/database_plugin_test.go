package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

type fakeUsageStore struct {
	stats AggregatedStats
}

func (s *fakeUsageStore) Insert(context.Context, UsageRecord) error { return nil }

func (s *fakeUsageStore) InsertBatch(context.Context, []UsageRecord) (int64, int64, error) {
	return 0, 0, nil
}

func (s *fakeUsageStore) GetAggregatedStats(context.Context) (AggregatedStats, error) {
	return s.stats, nil
}

func (s *fakeUsageStore) GetDetails(context.Context, int, int) ([]DetailRecord, error) {
	return nil, nil
}

func (s *fakeUsageStore) DeleteOldRecords(context.Context, int) (int64, error) {
	return 0, nil
}

func (s *fakeUsageStore) EnsureSchema(context.Context) error { return nil }

func (s *fakeUsageStore) Close() error { return nil }

func TestGetCombinedSnapshot_StoreOnlySnapshotIgnoresMemory(t *testing.T) {
	oldStats := defaultRequestStatistics
	defer func() {
		defaultRequestStatistics = oldStats
	}()
	defaultRequestStatistics = NewRequestStatistics()
	SetStatisticsEnabled(true)

	defaultRequestStatistics.Record(context.Background(), coreusage.Record{
		APIKey:      "mem-api",
		Model:       "mem-model",
		RequestedAt: time.Now(),
		Detail: coreusage.Detail{
			TotalTokens: 99,
		},
	})

	now := time.Now().Add(-time.Hour)
	dbStats := AggregatedStats{
		TotalRequests: 3,
		SuccessCount:  2,
		FailureCount:  1,
		TotalTokens:   30,
		APIs: map[string]APIStats{
			"db-api": {
				TotalRequests: 3,
				TotalTokens:   30,
				Models: map[string]ModelStats{
					"db-model": {TotalRequests: 3, TotalTokens: 30},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-02-07": 3},
		RequestsByHour: map[string]int64{"10": 3},
		TokensByDay:    map[string]int64{"2026-02-07": 30},
		TokensByHour:   map[string]int64{"10": 30},
		Details: []DetailRecord{
			{
				APIKey:      "db-api",
				Model:       "db-model",
				Source:      "db-source",
				AuthIndex:   "0",
				Failed:      false,
				RequestedAt: now,
				TotalTokens: 10,
			},
		},
	}

	plugin := &DatabasePlugin{
		store:             &fakeUsageStore{stats: dbStats},
		storeOnlySnapshot: true,
	}

	snapshot := plugin.GetCombinedSnapshot()
	if snapshot.TotalRequests != dbStats.TotalRequests {
		t.Fatalf("unexpected total requests: got %d want %d", snapshot.TotalRequests, dbStats.TotalRequests)
	}
	if snapshot.TotalTokens != dbStats.TotalTokens {
		t.Fatalf("unexpected total tokens: got %d want %d", snapshot.TotalTokens, dbStats.TotalTokens)
	}
	if _, exists := snapshot.APIs["mem-api"]; exists {
		t.Fatalf("memory api should not be merged when storeOnlySnapshot is true")
	}
	if _, exists := snapshot.APIs["db-api"]; !exists {
		t.Fatalf("db api missing in snapshot")
	}
}
