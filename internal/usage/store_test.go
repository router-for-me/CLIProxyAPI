package usage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestMemoryStatisticsStoreSnapshotGeneration(t *testing.T) {
	store := NewMemoryStatisticsStore(NewRequestStatistics())
	timestamp := time.Date(2026, 3, 24, 9, 30, 0, 0, time.UTC)

	err := store.Record(context.Background(), coreusage.Record{
		Provider:    "openai",
		APIKey:      "api-1",
		Model:       "gpt-5",
		Source:      "tester",
		AuthIndex:   "1",
		RequestedAt: timestamp,
		Failed:      false,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 1 || snapshot.FailureCount != 0 {
		t.Fatalf("success/failure = %d/%d, want 1/0", snapshot.SuccessCount, snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want 30", snapshot.TotalTokens)
	}
	if snapshot.RequestsByDay["2026-03-24"] != 1 {
		t.Fatalf("requests by day missing expected bucket: %+v", snapshot.RequestsByDay)
	}
	if snapshot.RequestsByHour["09"] != 1 {
		t.Fatalf("requests by hour missing expected bucket: %+v", snapshot.RequestsByHour)
	}
	if snapshot.TokensByHour["09"] != 30 {
		t.Fatalf("tokens by hour missing expected bucket: %+v", snapshot.TokensByHour)
	}
}

func TestMemoryStatisticsStoreImportIdempotency(t *testing.T) {
	store := NewMemoryStatisticsStore(NewRequestStatistics())
	timestamp := time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC)

	payload := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"api-1": {
				Models: map[string]ModelSnapshot{
					"gpt-5": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							Source:    "tester",
							AuthIndex: "1",
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

	first, err := store.Import(context.Background(), payload)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if first.Added != 1 || first.Skipped != 0 {
		t.Fatalf("first import result = %+v, want added=1 skipped=0", first)
	}

	second, err := store.Import(context.Background(), payload)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if second.Added != 0 || second.Skipped != 1 {
		t.Fatalf("second import result = %+v, want added=0 skipped=1", second)
	}
}

func TestPostgresStatisticsStorePersistenceAndImportIdempotency(t *testing.T) {
	dsn := os.Getenv("USAGE_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("USAGE_POSTGRES_TEST_DSN not set")
	}

	tableName := fmt.Sprintf("usage_events_test_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := newPostgresStatisticsStore(ctx, dsn, true, tableName)
	if err != nil {
		t.Fatalf("create postgres store failed: %v", err)
	}
	defer func() {
		_, _ = store.db.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", quoteIdentifier(tableName)))
		_ = store.Close()
	}()

	timestamp := time.Date(2026, 3, 24, 11, 15, 0, 0, time.UTC)
	record := coreusage.Record{
		Provider:    "openai",
		APIKey:      "api-1",
		Model:       "gpt-5",
		Source:      "tester",
		AuthIndex:   "1",
		RequestedAt: timestamp,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	}
	if err = store.Record(ctx, record); err != nil {
		t.Fatalf("record failed: %v", err)
	}

	duplicate := record
	duplicate.Latency = 3 * time.Second
	if err = store.Record(ctx, duplicate); err != nil {
		t.Fatalf("record duplicate failed: %v", err)
	}

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}

	if err = store.Close(); err != nil {
		t.Fatalf("close store failed: %v", err)
	}

	reopened, err := newPostgresStatisticsStore(ctx, dsn, false, tableName)
	if err != nil {
		t.Fatalf("reopen store failed: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	persisted, err := reopened.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot after reopen failed: %v", err)
	}
	if persisted.TotalRequests != 1 {
		t.Fatalf("persisted total requests = %d, want 1", persisted.TotalRequests)
	}

	importPayload := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"api-2": {
				Models: map[string]ModelSnapshot{
					"gpt-5": {
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC),
							Source:    "imported",
							AuthIndex: "2",
							Tokens: TokenStats{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
						}},
					},
				},
			},
		},
	}

	firstImport, err := reopened.Import(ctx, importPayload)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if firstImport.Added != 1 || firstImport.Skipped != 0 {
		t.Fatalf("first import result = %+v, want added=1 skipped=0", firstImport)
	}

	secondImport, err := reopened.Import(ctx, importPayload)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if secondImport.Added != 0 || secondImport.Skipped != 1 {
		t.Fatalf("second import result = %+v, want added=0 skipped=1", secondImport)
	}
}
