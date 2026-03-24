package usage

import (
	"context"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// MemoryStatisticsStore keeps usage statistics in-memory.
type MemoryStatisticsStore struct {
	stats *RequestStatistics
}

// NewMemoryStatisticsStore creates an in-memory usage store.
func NewMemoryStatisticsStore(stats *RequestStatistics) *MemoryStatisticsStore {
	if stats == nil {
		stats = NewRequestStatistics()
	}
	return &MemoryStatisticsStore{stats: stats}
}

func (s *MemoryStatisticsStore) Record(ctx context.Context, record coreusage.Record) error {
	if s == nil || s.stats == nil {
		return nil
	}
	s.stats.Record(ctx, record)
	return nil
}

func (s *MemoryStatisticsStore) Snapshot(context.Context) (StatisticsSnapshot, error) {
	if s == nil || s.stats == nil {
		return StatisticsSnapshot{}, nil
	}
	return s.stats.Snapshot(), nil
}

func (s *MemoryStatisticsStore) Export(ctx context.Context) (StatisticsSnapshot, error) {
	return s.Snapshot(ctx)
}

func (s *MemoryStatisticsStore) Import(_ context.Context, snapshot StatisticsSnapshot) (MergeResult, error) {
	if s == nil || s.stats == nil {
		return MergeResult{}, nil
	}
	return s.stats.MergeSnapshot(snapshot), nil
}

func (s *MemoryStatisticsStore) Close() error { return nil }
