package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRuntimeConfigSchemaCreated verifies that EnsureSchema materializes the
// runtime_config singleton table (PG-first control plane; Phase 1 Task 1). The
// table is created with a CHECK id = 1 singleton constraint and the canonical
// settings/extra/revision/updated_at/updated_by/updated_source columns. No row
// is seeded: callers (subsequent tasks) initialize the singleton on first
// write.
func TestRuntimeConfigSchemaCreated(t *testing.T) {
	pg := newTestPostgresStore(t, "test_runtime_config_schema")
	t.Cleanup(func() { _ = pg.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Schema name lives in the test isolation schema, so qualify the lookup.
	var tableExists bool
	if err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = $1 AND table_name = $2)`,
		pg.cfg.Schema, pg.cfg.RuntimeConfigTable,
	).Scan(&tableExists); err != nil {
		t.Fatalf("query runtime_config existence: %v", err)
	}
	if !tableExists {
		t.Fatalf("runtime_config table missing in schema %q", pg.cfg.Schema)
	}

	// Verify the canonical columns are present with the expected types. Pulled
	// individually so a missing column surfaces as its own diagnostic instead
	// of a generic scan failure.
	wantColumns := map[string]string{
		"id":             "integer",
		"settings":       "jsonb",
		"extra":          "jsonb",
		"revision":       "bigint",
		"updated_at":     "timestamp with time zone",
		"updated_by":     "text",
		"updated_source": "text",
	}
	for col, wantType := range wantColumns {
		var gotType string
		if err := pg.DB().QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
			pg.cfg.Schema, pg.cfg.RuntimeConfigTable, col,
		).Scan(&gotType); err != nil {
			t.Fatalf("runtime_config.%s column missing: %v", col, err)
		}
		if gotType != wantType {
			t.Fatalf("runtime_config.%s type = %q, want %q", col, gotType, wantType)
		}
	}

	// No row is seeded; the table exists but stays empty until callers
	// initialize the singleton on first write. Confirm the empty state.
	var rowCount int
	if err := pg.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+pg.RuntimeConfigTable(),
	).Scan(&rowCount); err != nil {
		t.Fatalf("count runtime_config rows: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("runtime_config row count = %d, want 0 (no seed)", rowCount)
	}
}

// TestRuntimeConfigSchemaEnsureIdempotent verifies that EnsureSchema can be
// invoked repeatedly without erroring, confirming the runtime_config CREATE
// TABLE IF NOT EXISTS + CHECK id = 1 DDL is safe to re-run on subsequent
// boot.
func TestRuntimeConfigSchemaEnsureIdempotent(t *testing.T) {
	pg := newTestPostgresStore(t, "test_runtime_config_idempotent")
	t.Cleanup(func() { _ = pg.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("first EnsureSchema: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("third EnsureSchema: %v", err)
	}

	// The CHECK id = 1 constraint must still be present after re-runs and must
	// reject an insert of id = 2.
	var constraintDef string
	if err := pg.DB().QueryRowContext(ctx, `SELECT check_clause FROM information_schema.check_constraints
		WHERE constraint_schema = $1 AND constraint_name LIKE 'runtime_config%'`,
		pg.cfg.Schema,
	).Scan(&constraintDef); err != nil {
		t.Fatalf("query runtime_config check constraint: %v", err)
	}
	if !strings.Contains(strings.ToLower(constraintDef), "id = 1") {
		t.Fatalf("runtime_config check constraint = %q, want contains \"id = 1\"", constraintDef)
	}

	if _, err := pg.DB().ExecContext(ctx,
		`INSERT INTO `+pg.RuntimeConfigTable()+` (id, settings, extra, revision) VALUES (2, '{}'::jsonb, '{}'::jsonb, 1)`,
	); err == nil {
		t.Fatal("expected CHECK id = 1 to reject id = 2 insert; got success")
	}
}

// TestRuntimeConfigTableAccessor verifies the fully-qualified table name
// returned by RuntimeConfigTable() matches the configured table and respects
// the schema prefix. The accessor delegates to fullTableName, which
// quote-identifier-wraps both the schema and the table name; the expected
// value must use the same quoting semantics so the comparison succeeds
// against a live Postgres.
func TestRuntimeConfigTableAccessor(t *testing.T) {
	pg := newTestPostgresStore(t, "test_runtime_config_accessor")
	t.Cleanup(func() { _ = pg.Close() })

	want := pg.fullTableName(pg.cfg.RuntimeConfigTable)
	got := pg.RuntimeConfigTable()
	if got != want {
		t.Fatalf("RuntimeConfigTable() = %q, want %q", got, want)
	}
}

// TestConfigRevisionsSchemaCreated verifies that EnsureSchema materializes the
// config_revisions append-only history table (PG-first control plane; Phase 1
// Task 2). The table is keyed by a monotonic BIGINT revision and stores the
// settings/resource_snapshot JSONB blobs, the checksum, and the audit
// metadata (created_at, created_by, reason). No row is seeded; writers append
// one row per accepted config change.
func TestConfigRevisionsSchemaCreated(t *testing.T) {
	pg := newTestPostgresStore(t, "test_config_revisions_schema")
	t.Cleanup(func() { _ = pg.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Schema name lives in the test isolation schema, so qualify the lookup.
	var tableExists bool
	if err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = $1 AND table_name = $2)`,
		pg.cfg.Schema, pg.cfg.ConfigRevisionsTable,
	).Scan(&tableExists); err != nil {
		t.Fatalf("query config_revisions existence: %v", err)
	}
	if !tableExists {
		t.Fatalf("config_revisions table missing in schema %q", pg.cfg.Schema)
	}

	// Verify the canonical columns are present with the expected types.
	wantColumns := map[string]string{
		"revision":          "bigint",
		"settings":          "jsonb",
		"resource_snapshot": "jsonb",
		"checksum":          "text",
		"created_at":        "timestamp with time zone",
		"created_by":        "text",
		"reason":            "text",
	}
	for col, wantType := range wantColumns {
		var gotType string
		if err := pg.DB().QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
			pg.cfg.Schema, pg.cfg.ConfigRevisionsTable, col,
		).Scan(&gotType); err != nil {
			t.Fatalf("config_revisions.%s column missing: %v", col, err)
		}
		if gotType != wantType {
			t.Fatalf("config_revisions.%s type = %q, want %q", col, gotType, wantType)
		}
	}

	// The idx_config_revisions_created_at index must exist for fast history
	// queries (and the test covers its name so a future rename is intentional).
	var indexExists bool
	if err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_indexes WHERE schemaname = $1 AND indexname = $2)`,
		pg.cfg.Schema, "idx_config_revisions_created_at",
	).Scan(&indexExists); err != nil {
		t.Fatalf("query config_revisions created_at index existence: %v", err)
	}
	if !indexExists {
		t.Fatalf("idx_config_revisions_created_at index missing in schema %q", pg.cfg.Schema)
	}

	// No row is seeded; the table exists but stays empty until writers append.
	var rowCount int
	if err := pg.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+pg.ConfigRevisionsTable(),
	).Scan(&rowCount); err != nil {
		t.Fatalf("count config_revisions rows: %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("config_revisions row count = %d, want 0 (no seed)", rowCount)
	}
}

// TestConfigRevisionsRoundTrip inserts two append-only revisions and reads
// them back ordered by created_at DESC. Verifies the checksum is persisted
// verbatim and that the ordering by created_at matches insertion order. This
// is the minimal contract writers of subsequent tasks will rely on; we do not
// pin JSONB shape here because that belongs to the writer layer.
func TestConfigRevisionsRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "test_config_revisions_roundtrip")
	t.Cleanup(func() { _ = pg.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Clear any prior runs; the schema-per-test isolation should make this a
	// no-op in fresh databases, but be defensive against shared schemas.
	if _, err := pg.DB().ExecContext(ctx,
		`DELETE FROM `+pg.ConfigRevisionsTable(),
	); err != nil {
		t.Fatalf("clear config_revisions: %v", err)
	}

	settingsA := `{"k":"a","n":1}`
	snapshotA := `{"providers":[]}`
	checksumA := "sha256:aaa"
	reasonA := "initial-seed"
	createdByA := "tester-a"

	settingsB := `{"k":"b","n":2}`
	snapshotB := `{"providers":[{"id":"p1"}]}`
	checksumB := "sha256:bbb"
	reasonB := "manual-edit"
	createdByB := "tester-b"

	if _, err := pg.DB().ExecContext(ctx,
		`INSERT INTO `+pg.ConfigRevisionsTable()+
			` (revision, settings, resource_snapshot, checksum, created_by, reason) `+
			`VALUES (1, $1::jsonb, $2::jsonb, $3, $4, $5)`,
		settingsA, snapshotA, checksumA, createdByA, reasonA,
	); err != nil {
		t.Fatalf("insert config_revisions revision 1: %v", err)
	}

	// Force a strict ordering signal: sleep one millisecond between inserts so
	// created_at can disambiguate rows whose wall clock would otherwise be the
	// same NOW() tick.
	time.Sleep(2 * time.Millisecond)

	if _, err := pg.DB().ExecContext(ctx,
		`INSERT INTO `+pg.ConfigRevisionsTable()+
			` (revision, settings, resource_snapshot, checksum, created_by, reason) `+
			`VALUES (2, $1::jsonb, $2::jsonb, $3, $4, $5)`,
		settingsB, snapshotB, checksumB, createdByB, reasonB,
	); err != nil {
		t.Fatalf("insert config_revisions revision 2: %v", err)
	}

	// Read back the two revisions ordered by created_at DESC (the index
	// direction) and assert ordering + checksum persistence. Pulling the rows
	// individually (rather than scanning into a slice) keeps the failure
	// surface narrow.
	row := pg.DB().QueryRowContext(ctx,
		`SELECT revision, checksum FROM `+pg.ConfigRevisionsTable()+
			` ORDER BY created_at DESC LIMIT 1`,
	)
	var newestRevision int64
	var newestChecksum string
	if err := row.Scan(&newestRevision, &newestChecksum); err != nil {
		t.Fatalf("scan newest config_revisions row: %v", err)
	}
	if newestRevision != 2 || newestChecksum != checksumB {
		t.Fatalf("newest revision/checksum = (%d, %q), want (2, %q)", newestRevision, newestChecksum, checksumB)
	}

	row = pg.DB().QueryRowContext(ctx,
		`SELECT revision, checksum FROM `+pg.ConfigRevisionsTable()+
			` ORDER BY created_at ASC LIMIT 1`,
	)
	var oldestRevision int64
	var oldestChecksum string
	if err := row.Scan(&oldestRevision, &oldestChecksum); err != nil {
		t.Fatalf("scan oldest config_revisions row: %v", err)
	}
	if oldestRevision != 1 || oldestChecksum != checksumA {
		t.Fatalf("oldest revision/checksum = (%d, %q), want (1, %q)", oldestRevision, oldestChecksum, checksumA)
	}

	// Sanity: ordering must be stable; the second-to-newest row is still
	// revision 1, not revision 2 again.
	row = pg.DB().QueryRowContext(ctx,
		`SELECT revision FROM `+pg.ConfigRevisionsTable()+
			` ORDER BY created_at DESC OFFSET 1 LIMIT 1`,
	)
	var secondRevision int64
	if err := row.Scan(&secondRevision); err != nil {
		t.Fatalf("scan second config_revisions row: %v", err)
	}
	if secondRevision != 1 {
		t.Fatalf("second-to-newest revision = %d, want 1", secondRevision)
	}
}

// TestConfigRevisionsTableAccessor verifies the fully-qualified table name
// returned by ConfigRevisionsTable() matches the configured table and
// respects the schema prefix. Mirrors TestRuntimeConfigTableAccessor.
func TestConfigRevisionsTableAccessor(t *testing.T) {
	pg := newTestPostgresStore(t, "test_config_revisions_accessor")
	t.Cleanup(func() { _ = pg.Close() })

	want := pg.fullTableName(pg.cfg.ConfigRevisionsTable)
	got := pg.ConfigRevisionsTable()
	if got != want {
		t.Fatalf("ConfigRevisionsTable() = %q, want %q", got, want)
	}
}
