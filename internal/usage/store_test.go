package usage

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveLocalUsageDBPath(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "auth")

	t.Setenv("PGSTORE_LOCAL_PATH", filepath.Join(t.TempDir(), "pglocal"))
	got := resolveLocalUsageDBPath(authDir)
	want := filepath.Join(getEnvOrFatal(t, "PGSTORE_LOCAL_PATH"), defaultLocalUsageFileName)
	if got != want {
		t.Fatalf("unexpected local db path: got %q want %q", got, want)
	}

	t.Setenv("PGSTORE_LOCAL_PATH", filepath.Join(t.TempDir(), "custom.db"))
	got = resolveLocalUsageDBPath(authDir)
	want = getEnvOrFatal(t, "PGSTORE_LOCAL_PATH")
	if got != want {
		t.Fatalf("unexpected db file path: got %q want %q", got, want)
	}

	t.Setenv("PGSTORE_LOCAL_PATH", "")
	got = resolveLocalUsageDBPath(authDir)
	want = filepath.Join(authDir, defaultLocalUsageFileName)
	if got != want {
		t.Fatalf("unexpected fallback db path: got %q want %q", got, want)
	}
}

func TestSQLiteUsageStoreReset(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sqlite", "usage.db")

	store, err := newSQLiteUsageStoreAtPath(dbPath)
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	defer store.Close()

	err = store.Insert(ctx, UsageRecord{
		APIKey:      "api-1",
		Model:       "model-1",
		Source:      "source-1",
		AuthIndex:   "0",
		Failed:      false,
		RequestedAt: time.Now(),
		TotalTokens: 10,
	})
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	details, err := store.GetDetails(ctx, 0, 10)
	if err != nil {
		t.Fatalf("GetDetails before reset failed: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("unexpected detail count before reset: got %d want 1", len(details))
	}

	if err = store.Reset(ctx); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	details, err = store.GetDetails(ctx, 0, 10)
	if err != nil {
		t.Fatalf("GetDetails after reset failed: %v", err)
	}
	if len(details) != 0 {
		t.Fatalf("unexpected detail count after reset: got %d want 0", len(details))
	}
}

func TestSQLiteUsageStorePersistsFailureReason(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sqlite", "usage.db")

	store, err := newSQLiteUsageStoreAtPath(dbPath)
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	defer store.Close()

	requestedAt := time.Now().Truncate(time.Second)
	err = store.Insert(ctx, UsageRecord{
		APIKey:             "api-1",
		Model:              "MiniMax-M2.7",
		Source:             "source-1",
		AuthIndex:          "3",
		RequestID:          "req-trace-1",
		AttemptNo:          2,
		RetryReason:        "upstream_timeout",
		FinalSuccess:       finalSuccessTrue,
		Failed:             true,
		RequestedAt:        requestedAt,
		Method:             "POST",
		Path:               "/v1/messages",
		ProviderStatusCode: http.StatusBadRequest,
		ErrorCode:          "invalid_request_error",
	})
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	details, err := store.GetDetails(ctx, 0, 10)
	if err != nil {
		t.Fatalf("GetDetails failed: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("detail count = %d, want 1", len(details))
	}
	if details[0].ProviderStatusCode != http.StatusBadRequest {
		t.Fatalf("provider status = %d, want %d", details[0].ProviderStatusCode, http.StatusBadRequest)
	}
	if details[0].ErrorCode != "invalid_request_error" {
		t.Fatalf("error code = %q, want invalid_request_error", details[0].ErrorCode)
	}
	if details[0].RequestID != "req-trace-1" || details[0].AttemptNo != 2 || details[0].RetryReason != "upstream_timeout" || details[0].FinalSuccess != finalSuccessTrue {
		t.Fatalf("request trace fields = (%q, %d, %q, %d), want (req-trace-1, 2, upstream_timeout, %d)", details[0].RequestID, details[0].AttemptNo, details[0].RetryReason, details[0].FinalSuccess, finalSuccessTrue)
	}

	logs, err := store.QueryMonitorRequestLogs(ctx, MonitorQueryFilter{Status: "failed"}, 1, 10, 3)
	if err != nil {
		t.Fatalf("QueryMonitorRequestLogs failed: %v", err)
	}
	if len(logs.Items) != 1 {
		t.Fatalf("monitor item count = %d, want 1", len(logs.Items))
	}
	if logs.Items[0].ProviderStatusCode != http.StatusBadRequest || logs.Items[0].ErrorCode != "invalid_request_error" {
		t.Fatalf("monitor failure reason = (%d, %q), want (%d, invalid_request_error)", logs.Items[0].ProviderStatusCode, logs.Items[0].ErrorCode, http.StatusBadRequest)
	}
	if logs.Items[0].RequestID != "req-trace-1" || logs.Items[0].AttemptNo != 2 || logs.Items[0].RetryReason != "upstream_timeout" || logs.Items[0].FinalSuccess == nil || !*logs.Items[0].FinalSuccess {
		t.Fatalf("monitor trace fields = (%q, %d, %q, %v), want successful request trace", logs.Items[0].RequestID, logs.Items[0].AttemptNo, logs.Items[0].RetryReason, logs.Items[0].FinalSuccess)
	}
}

func TestSQLiteUsageStoreUpdatesRequestFinalSuccess(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sqlite", "usage.db")

	store, err := newSQLiteUsageStoreAtPath(dbPath)
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	defer store.Close()

	requestedAt := time.Now().Truncate(time.Second)
	records := []UsageRecord{
		{APIKey: "api-1", Model: "gpt-5.5", Source: "source-a", AuthIndex: "auth-a", RequestID: "req-retry", AttemptNo: 1, FinalSuccess: finalSuccessUnknown, Failed: true, RequestedAt: requestedAt},
		{APIKey: "api-1", Model: "gpt-5.5", Source: "source-b", AuthIndex: "auth-b", RequestID: "req-retry", AttemptNo: 2, FinalSuccess: finalSuccessUnknown, Failed: true, RequestedAt: requestedAt.Add(time.Second)},
	}
	if _, _, err = store.InsertBatch(ctx, records); err != nil {
		t.Fatalf("InsertBatch failed: %v", err)
	}

	if err = store.UpdateRequestFinal(ctx, "req-retry", true); err != nil {
		t.Fatalf("UpdateRequestFinal failed: %v", err)
	}

	logs, err := store.QueryMonitorRequestLogs(ctx, MonitorQueryFilter{}, 1, 10, 3)
	if err != nil {
		t.Fatalf("QueryMonitorRequestLogs failed: %v", err)
	}
	if len(logs.Items) != 2 {
		t.Fatalf("monitor item count = %d, want 2", len(logs.Items))
	}
	for _, item := range logs.Items {
		if item.FinalSuccess == nil || !*item.FinalSuccess {
			t.Fatalf("final_success = %v, want true", item.FinalSuccess)
		}
	}
}

func TestSQLiteUsageStoreEnsureSchemaSkipsCoveredSingleIndexes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sqlite", "usage.db")

	store, err := newSQLiteUsageStoreAtPath(dbPath)
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	defer store.Close()

	names, err := sqliteIndexNameSet(ctx, store, "usage_records")
	if err != nil {
		t.Fatalf("sqliteIndexNameSet failed: %v", err)
	}

	if _, ok := names["idx_usage_requested_at"]; ok {
		t.Fatalf("unexpected redundant index created: idx_usage_requested_at")
	}
	if _, ok := names["idx_usage_api_key"]; ok {
		t.Fatalf("unexpected redundant index created: idx_usage_api_key")
	}
	if _, ok := names["idx_usage_requested_at_id"]; !ok {
		t.Fatalf("expected composite index missing: idx_usage_requested_at_id")
	}
	if _, ok := names["idx_usage_api_model"]; !ok {
		t.Fatalf("expected composite index missing: idx_usage_api_model")
	}
}

func TestSQLiteUsageStoreEnsureSchemaDropsLegacyCoveredSingleIndexes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sqlite", "usage.db")

	store, err := newSQLiteUsageStoreAtPath(dbPath)
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	defer store.Close()

	legacyIndexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_usage_requested_at ON usage_records(requested_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_usage_api_key ON usage_records(api_key)",
	}
	for _, query := range legacyIndexes {
		if _, err = store.db.ExecContext(ctx, query); err != nil {
			t.Fatalf("create legacy index failed: %v", err)
		}
	}

	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}

	names, err := sqliteIndexNameSet(ctx, store, "usage_records")
	if err != nil {
		t.Fatalf("sqliteIndexNameSet failed: %v", err)
	}

	if _, ok := names["idx_usage_requested_at"]; ok {
		t.Fatalf("legacy redundant index should be dropped: idx_usage_requested_at")
	}
	if _, ok := names["idx_usage_api_key"]; ok {
		t.Fatalf("legacy redundant index should be dropped: idx_usage_api_key")
	}
}

func sqliteIndexNameSet(ctx context.Context, store *sqliteUsageStore, tableName string) (map[string]struct{}, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type='index' AND tbl_name = ?
	`, tableName)
	if err != nil {
		return nil, fmt.Errorf("query sqlite indexes: %w", err)
	}
	defer rows.Close()

	names := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan sqlite index name: %w", err)
		}
		names[name] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite index names: %w", err)
	}
	return names, nil
}

func getEnvOrFatal(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("expected env %q to be set", key)
	}
	return value
}
