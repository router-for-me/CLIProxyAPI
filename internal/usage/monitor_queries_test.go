package usage

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestSQLiteUsageStoreQueryMonitorRequestLogs(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteUsageStore(t)
	defer store.Close()

	base := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	insertUsageRecords(t, store,
		UsageRecord{APIKey: "api-1", Model: "model-a", Source: "source-a", RequestedAt: base.Add(-3 * time.Hour), TotalTokens: 10},
		UsageRecord{APIKey: "api-1", Model: "model-a", Source: "source-a", RequestedAt: base.Add(-2 * time.Hour), Failed: true, TotalTokens: 20},
		UsageRecord{APIKey: "api-1", Model: "model-a", Source: "source-a", RequestedAt: base.Add(-1 * time.Hour), TotalTokens: 30},
		UsageRecord{APIKey: "api-2", Model: "model-b", Source: "source-b", RequestedAt: base.Add(-30 * time.Minute), TotalTokens: 40},
	)

	start := base.Add(-4 * time.Hour)
	end := base
	result, err := store.QueryMonitorRequestLogs(ctx, MonitorQueryFilter{
		APIContains: "api-1",
		Start:       &start,
		End:         &end,
	}, 2, 2, 3)
	if err != nil {
		t.Fatalf("QueryMonitorRequestLogs failed: %v", err)
	}

	if result.Total != 3 {
		t.Fatalf("unexpected total: got %d want 3", result.Total)
	}
	if result.Page != 2 || result.PageSize != 2 {
		t.Fatalf("unexpected page: page=%d pageSize=%d", result.Page, result.PageSize)
	}
	if len(result.Items) != 1 {
		t.Fatalf("unexpected item count: got %d want 1", len(result.Items))
	}
	if !result.Items[0].Timestamp.Equal(base.Add(-3 * time.Hour)) {
		t.Fatalf("unexpected item timestamp: got %s", result.Items[0].Timestamp)
	}

	stats, ok := result.GroupStats[MonitorGroupKey("source-a", "model-a")]
	if !ok {
		t.Fatalf("expected group stats for source-a/model-a")
	}
	if stats.Total != 3 || stats.Success != 2 {
		t.Fatalf("unexpected group stats: total=%d success=%d", stats.Total, stats.Success)
	}
	if len(stats.Recent) != 3 {
		t.Fatalf("unexpected recent count: %d", len(stats.Recent))
	}
	if !stats.Recent[0].Timestamp.Equal(base.Add(-3*time.Hour)) || !stats.Recent[2].Timestamp.Equal(base.Add(-1*time.Hour)) {
		t.Fatalf("recent order mismatch: %+v", stats.Recent)
	}

	assertStringSliceEqual(t, result.Filters.APIs, []string{"api-1"})
	assertStringSliceEqual(t, result.Filters.Models, []string{"model-a"})
	assertStringSliceEqual(t, result.Filters.Sources, []string{"source-a"})
}

func TestSQLiteUsageStoreQueryMonitorChannelStats(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteUsageStore(t)
	defer store.Close()

	base := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	insertUsageRecords(t, store,
		UsageRecord{APIKey: "api-1", Model: "model-a", Source: "source-a", RequestedAt: base.Add(-4 * time.Hour)},
		UsageRecord{APIKey: "api-1", Model: "model-a", Source: "source-a", RequestedAt: base.Add(-3 * time.Hour), Failed: true},
		UsageRecord{APIKey: "api-1", Model: "model-b", Source: "source-a", RequestedAt: base.Add(-2 * time.Hour)},
		UsageRecord{APIKey: "api-2", Model: "model-c", Source: "source-b", RequestedAt: base.Add(-1 * time.Hour)},
	)

	result, err := store.QueryMonitorChannelStats(ctx, MonitorQueryFilter{Status: "failed"}, 10, 12)
	if err != nil {
		t.Fatalf("QueryMonitorChannelStats failed: %v", err)
	}

	if len(result.Items) != 1 {
		t.Fatalf("unexpected item count: got %d want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Source != "source-a" {
		t.Fatalf("unexpected source: %s", item.Source)
	}
	if item.TotalRequests != 3 || item.SuccessRequests != 2 || item.FailedRequests != 1 {
		t.Fatalf("unexpected aggregate: %+v", item)
	}
	if len(item.Models) != 2 {
		t.Fatalf("unexpected model count: %d", len(item.Models))
	}
	if item.Models[0].Model != "model-a" || item.Models[0].Requests != 2 {
		t.Fatalf("unexpected first model: %+v", item.Models[0])
	}

	assertStringSliceEqual(t, result.Filters.Models, []string{"model-a", "model-b", "model-c"})
	assertStringSliceEqual(t, result.Filters.Sources, []string{"source-a", "source-b"})
}

func TestSQLiteUsageStoreQueryMonitorFailureStats(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteUsageStore(t)
	defer store.Close()

	base := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	insertUsageRecords(t, store,
		UsageRecord{APIKey: "api-1", Model: "model-a", Source: "source-a", RequestedAt: base.Add(-5 * time.Hour), Failed: true},
		UsageRecord{APIKey: "api-1", Model: "model-b", Source: "source-a", RequestedAt: base.Add(-4 * time.Hour), Failed: true},
		UsageRecord{APIKey: "api-1", Model: "model-b", Source: "source-a", RequestedAt: base.Add(-3 * time.Hour)},
		UsageRecord{APIKey: "api-2", Model: "model-c", Source: "source-b", RequestedAt: base.Add(-2 * time.Hour), Failed: true},
		UsageRecord{APIKey: "api-3", Model: "model-d", Source: "source-c", RequestedAt: base.Add(-1 * time.Hour)},
	)

	result, err := store.QueryMonitorFailureStats(ctx, MonitorQueryFilter{}, 2, 12)
	if err != nil {
		t.Fatalf("QueryMonitorFailureStats failed: %v", err)
	}

	if len(result.Items) != 2 {
		t.Fatalf("unexpected item count: got %d want 2", len(result.Items))
	}
	if result.Items[0].Source != "source-a" || result.Items[0].FailedCount != 2 {
		t.Fatalf("unexpected first item: %+v", result.Items[0])
	}
	if result.Items[1].Source != "source-b" || result.Items[1].FailedCount != 1 {
		t.Fatalf("unexpected second item: %+v", result.Items[1])
	}
	if len(result.Items[0].Models) == 0 || len(result.Items[1].Models) == 0 {
		t.Fatalf("expected models in failure items")
	}

	assertStringSliceEqual(t, result.Filters.Sources, []string{"source-a", "source-b"})
	assertStringSliceEqual(t, result.Filters.Models, []string{"model-a", "model-b", "model-c"})
}

func newTestSQLiteUsageStore(t *testing.T) *sqliteUsageStore {
	t.Helper()
	store, err := newSQLiteUsageStoreAtPath(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	return store
}

func insertUsageRecords(t *testing.T, store *sqliteUsageStore, records ...UsageRecord) {
	t.Helper()
	added, skipped, err := store.InsertBatch(context.Background(), records)
	if err != nil {
		t.Fatalf("InsertBatch failed: %v", err)
	}
	if added != int64(len(records)) || skipped != 0 {
		t.Fatalf("unexpected insert result: added=%d skipped=%d want_added=%d", added, skipped, len(records))
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	if len(gotCopy) != len(wantCopy) {
		t.Fatalf("slice length mismatch: got=%v want=%v", gotCopy, wantCopy)
	}
	for i := range gotCopy {
		if gotCopy[i] != wantCopy[i] {
			t.Fatalf("slice mismatch: got=%v want=%v", gotCopy, wantCopy)
		}
	}
}
