package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// roundTripTestStore opens a PostgresStore in an isolated schema and cleans the
// tables relevant to backup before returning it.
func roundTripTestStore(t *testing.T, schema string) *PostgresStore {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, PostgresStoreConfig{DSN: pgTestDSN(), Schema: schema})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return st
}

// seedBackupTestData inserts one API key + policy and one internal user so the
// export has realistic rows to dump.
func seedBackupTestData(t *testing.T, st *PostgresStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	apiKeys := NewAPIKeyStore(st)
	users := NewUserStore(st)

	if _, err := users.Create(ctx, InternalUser{
		ID:        "user-1",
		UserAlias: "alice",
		UserEmail: "alice@example.com",
		UserRole:  "internal_user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	rpm := 60
	if _, _, err := apiKeys.Create(ctx, "test-key", "test-alias", "sk-testsecret123", &expires, nil, &Policy{RPMLimit: &rpm}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	st := roundTripTestStore(t, "backup_rt_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seedBackupTestData(t, st)

	bundle, err := st.ExportData(ctx, BackupExportOpts{})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if bundle.Version != 2 {
		t.Fatalf("unexpected version %d", bundle.Version)
	}
	if _, ok := bundle.Resources["api_keys"]; !ok {
		t.Fatal("api_keys resource missing from export")
	}
	keysData := bundle.Resources["api_keys"]
	if len(keysData.Tables[st.APIKeysTable()]) != 1 {
		t.Fatalf("expected 1 api key row, got %d", len(keysData.Tables[st.APIKeysTable()]))
	}

	// Wipe and re-import into the same store.
	if err := st.clearAllResourceTables(ctx, AllBackupResources); err != nil {
		t.Fatalf("clear: %v", err)
	}
	report, err := st.ImportData(ctx, bundle, BackupImportOpts{})
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	if report.Total < 1 {
		t.Fatalf("expected at least one inserted row, got %d", report.Total)
	}

	// The restored API key hash must match the original, proving the row
	// (including its hashed secret) round-tripped byte-for-byte.
	keys, err := NewAPIKeyStore(st).List(ctx)
	if err != nil {
		t.Fatalf("List keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 restored api key, got %d", len(keys))
	}
	if got := keys[0].KeyHash; got != HashSecret("sk-testsecret123") {
		t.Fatalf("restored key hash %q != expected %q", got, HashSecret("sk-testsecret123"))
	}
}

func (s *PostgresStore) clearAllResourceTables(ctx context.Context, resources []BackupResource) error {
	for _, res := range resources {
		tables := s.resourceTables(res)
		for _, bt := range tables {
			if _, err := s.db.ExecContext(ctx, "DELETE FROM "+bt.name); err != nil {
				return err
			}
		}
	}
	return nil
}

// TestBackupImportResyncsIdSequence guards the export→import workflow against
// primary-key collisions on serial-id tables (usage_events / usage_errors
// among them). Import restores rows with their original explicit ids, which a
// fresh destination sequence does not know about; without a resync the next
// auto-generated insert collides with a restored id and fails — the exact
// symptom "usage events no longer recorded after full migration".
func TestBackupImportResyncsIdSequence(t *testing.T) {
	// One store, one schema — the shared default-schema shape of a real
	// migration (export from the prod DB, import into the same default schema on
	// the fresh host). Rows get the natural serial ids 1, 2, 3.
	st := roundTripTestStore(t, "backup_seq_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	us := NewUsageStore(st)
	seed := func(principal string, n int64) UsageEvent {
		ev := UsageEvent{
			APIKeyPrincipal: principal,
			Provider:        "anthropic",
			Model:           "claude-opus",
			InputTokens:     n * 1000,
			RequestedAt:     now(),
		}
		if err := us.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("seed %s: %v", principal, err)
		}
		return ev
	}
	seed("sk-1", 1)
	seed("sk-2", 2)
	seed("sk-3", 3)

	bundle, err := st.ExportData(ctx, BackupExportOpts{Resources: []BackupResource{ResourceUsage}})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}

	// Simulate the fresh live host: wipe the rows and reset the id sequence back
	// to its initial value (a brand-new DB's sequence starts at 1). Importing
	// must then resync the sequence past the restored ids.
	if err := st.clearAllResourceTables(ctx, []BackupResource{ResourceUsage}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('`+st.UsageEventsTable()+`', 'id'), 1, false)`); err != nil {
		t.Fatalf("reset sequence: %v", err)
	}
	if _, err := st.ImportData(ctx, bundle, BackupImportOpts{Resources: []BackupResource{ResourceUsage}}); err != nil {
		t.Fatalf("ImportData: %v", err)
	}

	// The next auto-generated insert must not collide with the restored ids 1..3.
	// Before the resync fix this failed with a unique-violation on id.
	ev := UsageEvent{
		APIKeyPrincipal: "sk-new",
		Provider:        "anthropic",
		Model:           "claude-opus",
		InputTokens:     500,
		RequestedAt:     now(),
	}
	if err := us.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("InsertEvent after import must succeed; got: %v", err)
	}

	// Both the restored rows and the new row must be present.
	total, err := us.SelectTotals(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("SelectTotals: %v", err)
	}
	if total.RequestCount != 4 {
		t.Fatalf("request count = %d; want 4 (3 restored + 1 new)", total.RequestCount)
	}
}

// TestBackupFilterResources verifies that requesting a subset of resources only
// exports those resources.
func TestBackupFilterResources(t *testing.T) {
	st := roundTripTestStore(t, "backup_filt_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	seedBackupTestData(t, st)

	bundle, err := st.ExportData(ctx, BackupExportOpts{Resources: []BackupResource{ResourceAPIKeys}})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if _, ok := bundle.Resources["api_keys"]; !ok {
		t.Fatal("api_keys resource expected in export")
	}
	if _, ok := bundle.Resources["internal_users"]; ok {
		t.Fatal("internal_users should not be exported when filtered out")
	}
}

// TestBackupJSONDeterminism ensures the exported rows are valid JSON that can be
// marshaled/unmarshaled cleanly (the transport format).
func TestBackupJSONRoundTrip(t *testing.T) {
	st := roundTripTestStore(t, "backup_json_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	seedBackupTestData(t, st)
	bundle, err := st.ExportData(ctx, BackupExportOpts{})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	enc, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	var decoded BackupBundle
	if err := json.Unmarshal(enc, &decoded); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	// Import from the unmarshaled copy to prove transport survives JSON.
	if _, err := st.ImportData(ctx, decoded, BackupImportOpts{}); err != nil {
		t.Fatalf("ImportData from JSON: %v", err)
	}
}

// usageEventJSON builds a single valid usage_events row for the given explicit
// serial id. Every NOT NULL column is supplied because jsonb_populate_recordset
// does not apply column defaults to absent keys.
func usageEventJSON(id int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"id":%d,"request_id":"req-%d","api_key_principal":"sk-test","provider":"anthropic","model":"claude-opus","input_tokens":10,"output_tokens":5,"reasoning_tokens":0,"cached_tokens":0,"cache_creation_tokens":0,"total_tokens":15,"cost_usd":0.001,"failed":false,"generate":false,"requested_at":"2026-08-15T00:00:00Z","flushed_at":"2026-08-15T00:00:00Z","discount_pct":0,"original_cost_usd":0.001}`,
		id, id))
}

// usageEventBundle assembles a minimal BackupBundle holding only the usage
// resource's usage_events rows, keyed by the store's schema-qualified table name
// so it matches what resourceTables(ResourceUsage) produces.
func usageEventBundle(st *PostgresStore, rows []json.RawMessage) BackupBundle {
	return BackupBundle{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Resources: map[string]BackupResourceData{
			string(ResourceUsage): {
				Tables: map[string][]json.RawMessage{
					st.UsageEventsTable(): rows,
				},
			},
		},
	}
}

// TestBackupImportChunkedProgress verifies that importing a data resource with
// more rows than importBatchSize splits the inserts into per-chunk transactions,
// fires the Progress callback with cumulative inserted counts, and reports the
// full inserted total.
func TestBackupImportChunkedProgress(t *testing.T) {
	st := roundTripTestStore(t, "backup_chunk_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const total = importBatchSize + 1500 // > one batch, forces chunking
	rows := make([]json.RawMessage, 0, total)
	for i := int64(1); i <= total; i++ {
		rows = append(rows, usageEventJSON(i))
	}

	var progressCalls []int
	report, err := st.ImportData(ctx, usageEventBundle(st, rows), BackupImportOpts{
		Resources: []BackupResource{ResourceUsage},
		Progress: func(inserted int) {
			progressCalls = append(progressCalls, inserted)
		},
	})
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	if len(progressCalls) == 0 {
		t.Fatal("expected at least one progress callback")
	}
	if got := report.Resources[ResourceUsage].Inserted; got != total {
		t.Fatalf("inserted = %d, want %d", got, total)
	}
	if got := report.Resources[ResourceUsage].Skipped; got != 0 {
		t.Fatalf("skipped = %d, want 0", got)
	}
	if report.Partial {
		t.Fatal("expected a clean import to not set the partial flag")
	}
	// Progress must be cumulative and strictly increasing, ending at the total.
	for i := 1; i < len(progressCalls); i++ {
		if progressCalls[i] <= progressCalls[i-1] {
			t.Fatalf("progress calls not cumulative: %v", progressCalls)
		}
	}
	if last := progressCalls[len(progressCalls)-1]; last != total {
		t.Fatalf("last progress call = %d, want %d", last, total)
	}
	// The actual rows must be present in the table.
	var count int
	if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+st.UsageEventsTable()).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != total {
		t.Fatalf("rows in table = %d, want %d", count, total)
	}
}

// TestBackupImportSoftFailureContinue verifies that a malformed row fails only
// its own chunk: the chunk is skipped and reported, the import continues past it,
// and the report is flagged partial while later chunks still land.
func TestBackupImportSoftFailureContinue(t *testing.T) {
	st := roundTripTestStore(t, "backup_chunk_soft_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const total = importBatchSize + 1500
	// A valid row inside the second chunk (indices [batch, 2*batch)) whose chunk
	// carries the malformed row below; it must be absent once the chunk is skipped.
	badChunkRow := int64(importBatchSize + 1)
	rows := make([]json.RawMessage, 0, total)
	for i := int64(1); i <= total; i++ {
		rows = append(rows, usageEventJSON(i))
	}
	// A non-integer into the BIGINT id column fails jsonb_populate_recordset and,
	// because the whole chunk is one multi-row INSERT, aborts just that chunk.
	rows[importBatchSize+500] = json.RawMessage(
		`{"id":"not-an-int","request_id":"req-bad","api_key_principal":"sk-test","provider":"anthropic","model":"claude-opus","input_tokens":10,"output_tokens":5,"reasoning_tokens":0,"cached_tokens":0,"cache_creation_tokens":0,"total_tokens":15,"cost_usd":0.001,"failed":false,"generate":false,"requested_at":"2026-08-15T00:00:00Z","flushed_at":"2026-08-15T00:00:00Z","discount_pct":0,"original_cost_usd":0.001}`)

	report, err := st.ImportData(ctx, usageEventBundle(st, rows), BackupImportOpts{
		Resources: []BackupResource{ResourceUsage},
	})
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	ur := report.Resources[ResourceUsage]
	if !report.Partial {
		t.Fatal("expected partial flag set after a skipped chunk")
	}
	if ur.Skipped < 1 {
		t.Fatalf("skipped = %d, want >= 1", ur.Skipped)
	}
	if ur.Inserted == 0 {
		t.Fatal("expected rows inserted after the soft failure (continue-to-end)")
	}
	if ur.Error == "" {
		t.Fatal("expected a soft error recorded on the resource report")
	}
	if ur.Inserted+ur.Skipped != total {
		t.Fatalf("inserted+skipped = %d, want %d", ur.Inserted+ur.Skipped, total)
	}
	// Rows in a chunk after the bad one must still be present (continue-to-end).
	var maxID int64
	if err := st.db.QueryRowContext(ctx, "SELECT MAX(id) FROM "+st.UsageEventsTable()).Scan(&maxID); err != nil {
		t.Fatalf("max id: %v", err)
	}
	if maxID != total {
		t.Fatalf("max id = %d, want %d (later chunks must continue)", maxID, total)
	}
	// The bad chunk was skipped entirely: a valid row inside it is absent.
	var badCount int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.UsageEventsTable()+" WHERE id = $1", badChunkRow).Scan(&badCount); err != nil {
		t.Fatalf("count bad-chunk row: %v", err)
	}
	if badCount != 0 {
		t.Fatalf("valid row id=%d inside the bad chunk is present; want skipped", badChunkRow)
	}
}

// TestBackupImportSoftFailureFirstChunkWipes verifies that when the first chunk
// fails, the wipe it carried is rolled back and retried on the next successful
// chunk. Pre-existing destination rows must be gone once the import completes,
// even though the first chunk was skipped.
func TestBackupImportSoftFailureFirstChunkWipes(t *testing.T) {
	st := roundTripTestStore(t, "backup_chunk_wipe_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Pre-existing destination rows that a successful import must remove.
	existing := usageEventJSON(1)
	if _, err := st.db.ExecContext(ctx,
		"INSERT INTO "+st.UsageEventsTable()+" SELECT * FROM jsonb_populate_record(NULL::"+st.UsageEventsTable()+", $1::jsonb)",
		string(existing)); err != nil {
		t.Fatalf("seed existing row: %v", err)
	}

	const total = importBatchSize + 100
	rows := make([]json.RawMessage, 0, total)
	for i := int64(1); i <= total; i++ {
		rows = append(rows, usageEventJSON(i))
	}
	// Malform the first chunk (id 5, inside indices [0, batch)).
	rows[5] = json.RawMessage(
		`{"id":"not-an-int","request_id":"req-bad","api_key_principal":"sk-test","provider":"anthropic","model":"claude-opus","input_tokens":10,"output_tokens":5,"reasoning_tokens":0,"cached_tokens":0,"cache_creation_tokens":0,"total_tokens":15,"cost_usd":0.001,"failed":false,"generate":false,"requested_at":"2026-08-15T00:00:00Z","flushed_at":"2026-08-15T00:00:00Z","discount_pct":0,"original_cost_usd":0.001}`)

	report, err := st.ImportData(ctx, usageEventBundle(st, rows), BackupImportOpts{
		Resources: []BackupResource{ResourceUsage},
	})
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	ur := report.Resources[ResourceUsage]
	if !report.Partial {
		t.Fatal("expected partial flag set after a skipped first chunk")
	}
	if ur.Inserted == 0 {
		t.Fatal("expected rows inserted after the first-chunk soft failure")
	}
	// The pre-existing row must be gone (the wipe was retried on a later chunk).
	var existingCount int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.UsageEventsTable()+" WHERE id = 1").Scan(&existingCount); err != nil {
		t.Fatalf("count existing row: %v", err)
	}
	if existingCount != 0 {
		t.Fatalf("pre-existing row id=1 present; wipe must be retried after a skipped first chunk")
	}
}

// usageErrorJSON builds a single valid usage_errors row for the given explicit
// serial id. It mirrors usageEventJSON but includes the error_message column,
// which is NOT NULL on usage_errors (and absent from usage_events).
func usageErrorJSON(id int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"id":%d,"request_id":"req-%d","api_key_principal":"sk-test","provider":"anthropic","model":"claude-opus","input_tokens":10,"output_tokens":5,"reasoning_tokens":0,"cached_tokens":0,"cache_creation_tokens":0,"total_tokens":15,"cost_usd":0.001,"error_message":"boom","failed":true,"fail_status_code":500,"generate":false,"requested_at":"2026-08-15T00:00:00Z","flushed_at":"2026-08-15T00:00:00Z","discount_pct":0,"original_cost_usd":0.001}`,
		id, id))
}

// TestBackupImportConfigAllOrNothing verifies that a malformed config row
// aborts the entire config phase: prior config resources that already imported
// inside the single transaction are rolled back too (all-or-nothing across
// config resources), and the data phase never runs.
func TestBackupImportConfigAllOrNothing(t *testing.T) {
	st := roundTripTestStore(t, "backup_cfg_rollback_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Two config resources in one bundle: api_keys (imported first) and
	// internal_users (carries a malformed row). The api_keys rows must be rolled
	// back when internal_users fails.
	apiKeyRow := json.RawMessage(
		`{"id":"ak-1","name":"test","key_hash":"hash-1","key_prefix":"sk-","status":"active","created_at":"2026-08-15T00:00:00Z","updated_at":"2026-08-15T00:00:00Z","metadata":{}}`)
	// All NOT NULL backfill columns on api_key_policies must be supplied
	// (model_routes, allowed_ips, blocked_ips) because jsonb_populate_recordset
	// does not apply column defaults.
	policyRow := json.RawMessage(
		`{"api_key_id":"ak-1","allowed_models":[],"blocked_models":[],"model_routes":[],"allowed_ips":[],"blocked_ips":[],"updated_at":"2026-08-15T00:00:00Z"}`)
	// A non-numeric into the NUMERIC spend column fails jsonb_populate_recordset.
	badUser := json.RawMessage(
		`{"id":"u-bad","user_alias":"bad","user_role":"internal_user","models":[],"metadata":{},"spend":"not-a-number","created_at":"2026-08-15T00:00:00Z","updated_at":"2026-08-15T00:00:00Z"}`)
	// A data row (usage) that must never be inserted because the config phase
	// aborts before the data phase runs.
	usageRow := usageEventJSON(1)

	bundle := BackupBundle{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Resources: map[string]BackupResourceData{
			string(ResourceAPIKeys): {
				Tables: map[string][]json.RawMessage{
					st.APIKeysTable():  {apiKeyRow},
					st.PoliciesTable(): {policyRow},
				},
			},
			string(ResourceInternalUsers): {
				Tables: map[string][]json.RawMessage{
					st.InternalUsersTable(): {badUser},
				},
			},
			string(ResourceUsage): {
				Tables: map[string][]json.RawMessage{
					st.UsageEventsTable(): {usageRow},
				},
			},
		},
	}

	var progressCalls []int
	report, err := st.ImportData(ctx, bundle, BackupImportOpts{
		Progress: func(n int) { progressCalls = append(progressCalls, n) },
	})
	if err == nil {
		t.Fatal("expected ImportData to fail on the malformed config row")
	}
	// The data phase must never run on a config failure, so no data progress
	// callback fires (and the config offset is never announced).
	if len(progressCalls) != 0 {
		t.Fatalf("expected no progress callbacks on config failure, got %v", progressCalls)
	}
	// The config transaction never committed, so the report must not claim any
	// inserted total (Tasks 6/7 read this on the error path too).
	if report.Total != 0 {
		t.Fatalf("report.Total = %d on config failure; want 0", report.Total)
	}

	// All-or-nothing across config resources: the api_keys rows that imported
	// before the internal_users failure must be rolled back with the transaction.
	var keyCount int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.APIKeysTable()+" WHERE id = 'ak-1'").Scan(&keyCount); err != nil {
		t.Fatalf("count api key: %v", err)
	}
	if keyCount != 0 {
		t.Fatalf("api key rolled back expected; found %d row(s)", keyCount)
	}
	var userCount int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.InternalUsersTable()).Scan(&userCount); err != nil {
		t.Fatalf("count internal users: %v", err)
	}
	if userCount != 0 {
		t.Fatalf("internal user rolled back expected; found %d row(s)", userCount)
	}
	// The data phase never ran: the usage row is absent.
	var usageCount int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.UsageEventsTable()).Scan(&usageCount); err != nil {
		t.Fatalf("count usage rows: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("data phase ran despite config failure; found %d usage row(s)", usageCount)
	}
}

// TestBackupImportMultiTableProgress verifies that the Progress callback stays
// strictly increasing across table boundaries within one data resource (usage
// spans usage_events + usage_errors), with no reset at the boundary.
func TestBackupImportMultiTableProgress(t *testing.T) {
	st := roundTripTestStore(t, "backup_chunk_multi_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const events = importBatchSize + 500 // forces at least two chunks
	const errors = 200
	eventRows := make([]json.RawMessage, 0, events)
	for i := int64(1); i <= events; i++ {
		eventRows = append(eventRows, usageEventJSON(i))
	}
	errorRows := make([]json.RawMessage, 0, errors)
	for i := int64(1); i <= errors; i++ {
		errorRows = append(errorRows, usageErrorJSON(events+int64(i))) // distinct ids
	}
	bundle := BackupBundle{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Resources: map[string]BackupResourceData{
			string(ResourceUsage): {
				Tables: map[string][]json.RawMessage{
					st.UsageEventsTable(): eventRows,
					st.UsageErrorsTable(): errorRows,
				},
			},
		},
	}

	var progressCalls []int
	report, err := st.ImportData(ctx, bundle, BackupImportOpts{
		Resources: []BackupResource{ResourceUsage},
		Progress: func(n int) {
			progressCalls = append(progressCalls, n)
		},
	})
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	if got := report.Resources[ResourceUsage].Inserted; got != events+errors {
		t.Fatalf("inserted = %d, want %d", got, events+errors)
	}
	if len(progressCalls) < 3 {
		t.Fatalf("expected >=3 progress calls (events chunks + errors chunk), got %d: %v", len(progressCalls), progressCalls)
	}
	// Strictly increasing with no reset or decrease at the usage_events ->
	// usage_errors boundary.
	for i := 1; i < len(progressCalls); i++ {
		if progressCalls[i] <= progressCalls[i-1] {
			t.Fatalf("progress not strictly increasing at call %d: %v", i, progressCalls)
		}
	}
	if last := progressCalls[len(progressCalls)-1]; last != events+errors {
		t.Fatalf("last progress call = %d, want %d", last, events+errors)
	}
	// Both tables must hold their rows.
	var evCount, erCount int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.UsageEventsTable()).Scan(&evCount); err != nil {
		t.Fatalf("count usage_events: %v", err)
	}
	if evCount != events {
		t.Fatalf("usage_events count = %d, want %d", evCount, events)
	}
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+st.UsageErrorsTable()).Scan(&erCount); err != nil {
		t.Fatalf("count usage_errors: %v", err)
	}
	if erCount != errors {
		t.Fatalf("usage_errors count = %d, want %d", erCount, errors)
	}
}

// TestBackupImportProgressIncludesConfig verifies that the Progress callback
// counts config rows as well: it fires once after the config phase commits (the
// config total) and again after each data chunk, with the running total. The
// final call must equal the combined config+data total, so a progress bar scaled
// to the bundle summary reaches 100%.
func TestBackupImportProgressIncludesConfig(t *testing.T) {
	st := roundTripTestStore(t, "backup_cfg_progress_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// One config resource (api_keys: 1 key + 1 policy) plus a small usage set.
	apiKeyRow := json.RawMessage(
		`{"id":"ak-1","name":"test","key_hash":"hash-1","key_prefix":"sk-","status":"active","created_at":"2026-08-15T00:00:00Z","updated_at":"2026-08-15T00:00:00Z","metadata":{}}`)
	policyRow := json.RawMessage(
		`{"api_key_id":"ak-1","allowed_models":[],"blocked_models":[],"model_routes":[],"allowed_ips":[],"blocked_ips":[],"updated_at":"2026-08-15T00:00:00Z"}`)
	const events = importBatchSize + 100 // forces chunking for the data phase
	eventRows := make([]json.RawMessage, 0, events)
	for i := int64(1); i <= events; i++ {
		eventRows = append(eventRows, usageEventJSON(i))
	}

	bundle := BackupBundle{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Resources: map[string]BackupResourceData{
			string(ResourceAPIKeys): {
				Tables: map[string][]json.RawMessage{
					st.APIKeysTable():  {apiKeyRow},
					st.PoliciesTable(): {policyRow},
				},
			},
			string(ResourceUsage): {
				Tables: map[string][]json.RawMessage{
					st.UsageEventsTable(): eventRows,
				},
			},
		},
	}

	const configTotal = 2 // 1 api key + 1 policy
	var progressCalls []int
	report, err := st.ImportData(ctx, bundle, BackupImportOpts{
		Progress: func(n int) { progressCalls = append(progressCalls, n) },
	})
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	// First call must announce the committed config total; subsequent calls must
	// stay strictly increasing and end at the combined total.
	if len(progressCalls) < 2 {
		t.Fatalf("expected >=2 progress calls (config + data chunks), got %d: %v", len(progressCalls), progressCalls)
	}
	if progressCalls[0] != configTotal {
		t.Fatalf("first progress call = %d, want config total %d", progressCalls[0], configTotal)
	}
	for i := 1; i < len(progressCalls); i++ {
		if progressCalls[i] <= progressCalls[i-1] {
			t.Fatalf("progress not strictly increasing at call %d: %v", i, progressCalls)
		}
	}
	want := configTotal + events
	if last := progressCalls[len(progressCalls)-1]; last != want {
		t.Fatalf("last progress call = %d, want %d", last, want)
	}
	if report.Total != want {
		t.Fatalf("report.Total = %d, want %d", report.Total, want)
	}
}

// TestBackupSummaryCounts verifies the bundle header embeds a per-resource
// row-count summary that matches the actual exported rows, so the frontend can
// render a pre-import preview without parsing every row.
func TestBackupSummaryCounts(t *testing.T) {
	st := roundTripTestStore(t, "backup_sum_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seedBackupTestData(t, st)

	bundle, err := st.ExportData(ctx, BackupExportOpts{})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if got := bundle.Summary["api_keys"]; got != 2 { // 1 api key + 1 policy row
		t.Fatalf("api_keys summary = %d, want 2", got)
	}
	if got := bundle.Summary["internal_users"]; got != 1 {
		t.Fatalf("internal_users summary = %d, want 1", got)
	}
}

// TestBackupStreamExportKeysMatch verifies that the streaming export emits a
// valid BackupBundle with the same resource/table keys as the in-memory export,
// and that the header summary matches too. It does not assert full equality of
// the (potentially large) row payloads.
func TestBackupStreamExportKeysMatch(t *testing.T) {
	st := roundTripTestStore(t, "backup_str_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seedBackupTestData(t, st)

	mem, err := st.ExportData(ctx, BackupExportOpts{})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	var buf bytes.Buffer
	if err := st.StreamExport(ctx, BackupExportOpts{}, &buf); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	var streamed BackupBundle
	if err := json.Unmarshal(buf.Bytes(), &streamed); err != nil {
		t.Fatalf("unmarshal streamed: %v", err)
	}
	for res, data := range mem.Resources {
		sd, ok := streamed.Resources[res]
		if !ok {
			t.Fatalf("streamed missing resource %q", res)
		}
		for tbl, rows := range data.Tables {
			srows, ok := sd.Tables[tbl]
			if !ok {
				t.Fatalf("streamed missing table %q in %q", tbl, res)
			}
			// Row counts must match per table so a regression that silently drops
			// rows in the streaming dump is caught (the header summary alone would
			// still match, since it is computed by a separate COUNT pass).
			if len(srows) != len(rows) {
				t.Fatalf("streamed table %q in %q has %d rows, want %d", tbl, res, len(srows), len(rows))
			}
		}
	}
	// The header summary must match the in-memory export so the frontend preview
	// reads identical counts from either path.
	if len(streamed.Summary) != len(mem.Summary) {
		t.Fatalf("streamed summary size = %d, want %d", len(streamed.Summary), len(mem.Summary))
	}
	for res, n := range mem.Summary {
		if got := streamed.Summary[res]; got != n {
			t.Fatalf("streamed summary[%q] = %d, want %d", res, got, n)
		}
	}
}

// TestBackupStreamExportPricingFiles verifies that the streaming export embeds
// on-disk pricing-source catalog files exactly like the in-memory export: the
// ,"files":{...} object must be comma-framed correctly relative to the tables
// object, omitted when there are no file-backed sources, and base64-encode the
// file content keyed by its absolute path.
func TestBackupStreamExportPricingFiles(t *testing.T) {
	st := roundTripTestStore(t, "backup_str_files_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	content := []byte(`{"models":[{"name":"m"}]}`)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	ps := NewPricingSourceStore(st)
	if _, err := ps.Create(ctx, PricingSource{Name: "file-src", SourceType: "file", FilePath: filePath, Format: "litellm", Enabled: true}); err != nil {
		t.Fatalf("create file pricing source: %v", err)
	}
	// A url-backed source must not produce a files entry.
	if _, err := ps.Create(ctx, PricingSource{Name: "url-src", SourceType: "url", URL: "https://example.com/c.json", Format: "litellm", Enabled: true}); err != nil {
		t.Fatalf("create url pricing source: %v", err)
	}

	var buf bytes.Buffer
	if err := st.StreamExport(ctx, BackupExportOpts{Resources: []BackupResource{ResourcePricingSources}}, &buf); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	var streamed BackupBundle
	if err := json.Unmarshal(buf.Bytes(), &streamed); err != nil {
		t.Fatalf("unmarshal streamed: %v", err)
	}
	rd := streamed.Resources[string(ResourcePricingSources)]
	got, ok := rd.Files[filePath]
	if !ok {
		t.Fatalf("streamed pricing source files missing %q (have %v)", filePath, rd.Files)
	}
	if got != base64.StdEncoding.EncodeToString(content) {
		t.Fatalf("streamed file content = %q, want base64 of %q", got, content)
	}
	if len(rd.Files) != 1 {
		t.Fatalf("streamed files = %v, want only the file-backed source", rd.Files)
	}

	// The in-memory export must embed the same file, proving parity between paths.
	mem, err := st.ExportData(ctx, BackupExportOpts{Resources: []BackupResource{ResourcePricingSources}})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	md := mem.Resources[string(ResourcePricingSources)]
	if mgot := md.Files[filePath]; mgot != got {
		t.Fatalf("in-memory file content = %q, streamed = %q", mgot, got)
	}
}

// TestBackupStreamExportOmitsEmptyFiles verifies that the streamed document for
// resources with no file-backed sources stays valid JSON and carries no "files"
// object (matching ExportData's omitempty behavior), so the ","files": framing
// never produces trailing-comma corruption when there is nothing to embed.
func TestBackupStreamExportOmitsEmptyFiles(t *testing.T) {
	st := roundTripTestStore(t, "backup_str_nofiles_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seedBackupTestData(t, st)

	var buf bytes.Buffer
	if err := st.StreamExport(ctx, BackupExportOpts{Resources: []BackupResource{ResourceAPIKeys}}, &buf); err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte(`"files"`)) {
		t.Fatalf("streamed api_keys export should not contain a files object:\n%s", buf.Bytes())
	}
	var streamed BackupBundle
	if err := json.Unmarshal(buf.Bytes(), &streamed); err != nil {
		t.Fatalf("unmarshal streamed: %v", err)
	}
	if len(streamed.Resources[string(ResourceAPIKeys)].Files) != 0 {
		t.Fatalf("streamed api_keys files = %v, want none", streamed.Resources[string(ResourceAPIKeys)].Files)
	}
}

// TestAllBackupResourcesIncludesMissingTables verifies that the five tables
// previously absent from the backup bundle (config_store, cooldown_store,
// usage_stat_day, model_routing, management_audit_log) are covered by
// AllBackupResources and mapped to the right schema-qualified table names.
func TestAllBackupResourcesIncludesMissingTables(t *testing.T) {
	st := &PostgresStore{cfg: PostgresStoreConfig{
		Schema:                  "backup_missing",
		ConfigTable:             "config_store",
		RuntimeConfigTable:      "runtime_config",
		ConfigRevisionsTable:    "config_revisions",
		ConfigImportsTable:      "config_imports",
		CooldownTable:           "cooldown_store",
		UsageStatDayTable:       "usage_stat_day",
		ModelRoutingTable:       "model_routing",
		ManagementAuditLogTable: "management_audit_log",
	}}

	newResources := []BackupResource{
		ResourceConfigStore,
		ResourceRuntimeConfig,
		ResourceConfigRevisions,
		ResourceConfigImports,
		ResourceCooldownStore,
		ResourceUsageStatDay,
		ResourceModelRouting,
		ResourceManagementAuditLog,
	}
	seen := make(map[BackupResource]bool, len(AllBackupResources))
	for _, r := range AllBackupResources {
		seen[r] = true
	}
	for _, r := range newResources {
		if !seen[r] {
			t.Errorf("AllBackupResources missing %q", r)
		}
	}

	want := map[BackupResource]struct {
		name        string
		orderColumn string
	}{
		ResourceConfigStore:        {`"backup_missing"."config_store"`, "id"},
		ResourceRuntimeConfig:      {`"backup_missing"."runtime_config"`, "id"},
		ResourceConfigRevisions:    {`"backup_missing"."config_revisions"`, "revision"},
		ResourceConfigImports:      {`"backup_missing"."config_imports"`, "id"},
		ResourceCooldownStore:      {`"backup_missing"."cooldown_store"`, ""},
		ResourceUsageStatDay:       {`"backup_missing"."usage_stat_day"`, "stat_day"},
		ResourceModelRouting:       {`"backup_missing"."model_routing"`, "id"},
		ResourceManagementAuditLog: {`"backup_missing"."management_audit_log"`, "id"},
	}
	for res, exp := range want {
		tables := st.resourceTables(res)
		if len(tables) != 1 {
			t.Errorf("resourceTables(%q) returned %d tables, want 1", res, len(tables))
			continue
		}
		if tables[0].name != exp.name {
			t.Errorf("resourceTables(%q)[0].name = %q, want %q", res, tables[0].name, exp.name)
		}
		if tables[0].orderColumn != exp.orderColumn {
			t.Errorf("resourceTables(%q)[0].orderColumn = %q, want %q", res, tables[0].orderColumn, exp.orderColumn)
		}
	}

	// Chunked (data) vs atomic (config): usage_stat_day and the audit log can
	// grow large so they import in per-chunk transactions; the others restore
	// atomically.
	wantData := map[BackupResource]bool{
		ResourceConfigStore:        false,
		ResourceCooldownStore:      false,
		ResourceUsageStatDay:       true,
		ResourceModelRouting:       false,
		ResourceManagementAuditLog: true,
	}
	for res, exp := range wantData {
		if got := dataResource(res); got != exp {
			t.Errorf("dataResource(%q) = %v, want %v", res, got, exp)
		}
	}
}

func randSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// TestBackupRequestBodiesRoundTrip pins the request_bodies registration on
// ResourceUsage and the four sealed body columns: the export must contain the
// unsealed plaintext body, and importing it back must restore a readable row.
func TestBackupRequestBodiesRoundTrip(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	key := []byte("0123456789abcdef0123456789abcdef")
	st, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:                pgTestDSN(),
		Schema:             "backup_reqbodies_" + randSuffix(),
		UsageEncryptionKey: key,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	sealer, err := NewSealer(key)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	usage := NewUsageStore(st)
	usage.SetSealer(sealer)

	rb := RequestBody{
		RequestID:             "req-rt",
		Provider:              "claude",
		ClientRequestHeaders:  `{"Content-Type":["application/json"]}`,
		ClientRequestBody:     `{"prompt":"secret"}`,
		ClientResponseHeaders: `{"Content-Type":["text/event-stream"]}`,
		ClientResponseBody:    `data: hello`,
		UpstreamRequest:       `POST /v1/messages {"prompt":"secret"}`,
		UpstreamResponse:      `{"content":[{"text":"hi"}]}`,
	}
	if err := usage.InsertRequestBody(ctx, rb); err != nil {
		t.Fatalf("insert: %v", err)
	}

	bundle, err := st.ExportData(ctx, BackupExportOpts{Resources: []BackupResource{ResourceUsage}})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	rows := bundle.Resources[string(ResourceUsage)].Tables[st.RequestBodiesTable()]
	if len(rows) != 1 {
		t.Fatalf("request_bodies rows = %d, want 1", len(rows))
	}
	var exported map[string]any
	if err := json.Unmarshal(rows[0], &exported); err != nil {
		t.Fatalf("decode exported row: %v", err)
	}
	if gotBody, _ := exported["client_request_body"].(string); gotBody != rb.ClientRequestBody {
		t.Fatalf("exported client_request_body = %q, want unsealed plaintext %q", gotBody, rb.ClientRequestBody)
	}

	if err := st.clearAllResourceTables(ctx, []BackupResource{ResourceUsage}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := st.ImportData(ctx, bundle, BackupImportOpts{Resources: []BackupResource{ResourceUsage}}); err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	got, err := usage.GetRequestBodyByRequestID(ctx, "req-rt")
	if err != nil {
		t.Fatalf("get after import: %v", err)
	}
	if got.ClientRequestBody != rb.ClientRequestBody || got.UpstreamResponse != rb.UpstreamResponse {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestBackupBundleV2JSON(t *testing.T) {
	b := BackupBundle{
		Version:    backupBundleVersion,
		ExportedAt: time.Now().UTC(),
		Mode:       "full",
		Summary:    map[string]int{"config": 1, "auth_files": 1},
		Resources:  map[string]BackupResourceData{},
		ConfigYAML: "api_key: sk-test\n",
		AuthFiles: []BackupAuthFile{
			{Path: "openai.json", Content: `{"api_key":"x"}`},
		},
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var back BackupBundle
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Version != backupBundleVersion {
		t.Errorf("version round-trip failed: %d", back.Version)
	}
	if back.Mode != "full" {
		t.Errorf("mode round-trip failed: %q", back.Mode)
	}
	if back.ConfigYAML != b.ConfigYAML {
		t.Errorf("config_yaml round-trip failed: %q", back.ConfigYAML)
	}
	if len(back.AuthFiles) != 1 || back.AuthFiles[0].Path != "openai.json" || back.AuthFiles[0].Content != `{"api_key":"x"}` {
		t.Errorf("auth_files round-trip failed: %+v", back.AuthFiles)
	}
}
