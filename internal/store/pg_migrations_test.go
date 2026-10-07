package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ensureMigrated runs every registered idempotent migration for the schema
// returned by NewPostgresStore. Tests that rely on tables/indexes added via
// migrations must call it after constructing the store. A 30s timeout mirrors
// newTestPostgresStore so a hung migration fails fast instead of hanging.
func ensureMigrated(t *testing.T, pg *PostgresStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

// TestMigrateIdempotent verifies that running every registered migration twice
// remains a no-op after the first successful run. The concrete DDL assertions
// live in TestMigrateCreatesUsageIndexes and TestMigrateUpstreamProviderEntryIdentity.
func TestMigrateIdempotent(t *testing.T) {
	pg := newTestPostgresStore(t, "test_migrate")
	defer pg.Close()
	ensureMigrated(t, pg)
	// Running Migrate a second time must be a no-op (idempotent).
	ensureMigrated(t, pg)
}

// TestMigrateCreatesUsageIndexes asserts that Migrate materializes the three
// usage_events aggregate indexes (and that they are recorded in pg_indexes for
// the resolved schema). Running Migrate twice and checking the count stays at 3
// also proves the CREATE INDEX IF NOT EXISTS statements are idempotent.
func TestMigrateCreatesUsageIndexes(t *testing.T) {
	pg := newTestPostgresStore(t, "test_migrate_usage_idx")
	defer pg.Close()
	ensureMigrated(t, pg)

	wantIndexes := []string{
		"idx_usage_events_requested_at_user",
		"idx_usage_events_user_model_at",
		"idx_usage_events_provider",
	}
	// Migrate creates the indexes schema-qualified (via fullTableName), so the
	// schema is the store's configured schema rather than the connection's
	// current_schema() (the search_path is not set to the schema).
	schemaName := pg.cfg.Schema
	assertIndexes := func() {
		t.Helper()
		var count int
		if err := pg.DB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM pg_indexes
			 WHERE schemaname = $1 AND tablename = $2 AND indexname = ANY($3)`,
			schemaName, pg.cfg.UsageEventsTable, wantIndexes,
		).Scan(&count); err != nil {
			t.Fatalf("query pg_indexes: %v", err)
		}
		if count != len(wantIndexes) {
			t.Fatalf("expected %d usage_events indexes, found %d", len(wantIndexes), count)
		}
	}
	assertIndexes()

	// Running Migrate a second time must leave the index set unchanged,
	// proving the DDL is idempotent.
	ensureMigrated(t, pg)
	assertIndexes()
}

func TestMigrateUpstreamProviderEntryIdentity(t *testing.T) {
	pg := newTestPostgresStore(t, "test_migrate_upstream_entry_identity")
	defer pg.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var columnType string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'name'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&columnType); err != nil {
		t.Fatalf("query entry name column: %v", err)
	}
	if columnType != "text" {
		t.Fatalf("entry name column type = %q, want text", columnType)
	}

	var indexCount int
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pg_indexes
		WHERE schemaname = $1 AND tablename = $2
		  AND indexname = 'idx_upstream_provider_entries_provider_name'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&indexCount); err != nil {
		t.Fatalf("query entry name index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("entry name unique index count = %d, want 1", indexCount)
	}
}

// TestUsageEventsEnergyAndMetadataColumns verifies the Neuralwatt billing
// columns are materialized by EnsureSchema, both fresh and on existing tables.
// energy_joules carries per-request energy consumption as NUMERIC(12,6);
// provider_metadata is a JSONB NOT NULL DEFAULT '{}' so callers can rely on a
// well-formed object without checking IS NULL.
func TestUsageEventsEnergyAndMetadataColumns(t *testing.T) {
	pg := newTestPostgresStore(t, "test_usage_events_energy_metadata")
	defer pg.Close()
	ensureMigrated(t, pg)
	ctx := context.Background()

	var dataType, isNullable string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'energy_joules'
	`, pg.cfg.Schema, pg.cfg.UsageEventsTable).Scan(&dataType, &isNullable); err != nil {
		t.Fatalf("energy_joules column missing: %v", err)
	}
	if dataType != "numeric" {
		t.Fatalf("energy_joules data_type = %q, want numeric", dataType)
	}

	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'provider_metadata'
	`, pg.cfg.Schema, pg.cfg.UsageEventsTable).Scan(&dataType, &isNullable); err != nil {
		t.Fatalf("provider_metadata column missing: %v", err)
	}
	if dataType != "jsonb" {
		t.Fatalf("provider_metadata data_type = %q, want jsonb", dataType)
	}
	if isNullable != "NO" {
		t.Fatalf("provider_metadata is_nullable = %q, want NO (NOT NULL DEFAULT)", isNullable)
	}

	// Re-running the schema bootstrap must leave the column set intact
	// (the ALTER ADD COLUMN IF NOT EXISTS statements are idempotent).
	ensureMigrated(t, pg)
	var count int
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		  AND column_name IN ('energy_joules', 'provider_metadata')
	`, pg.cfg.Schema, pg.cfg.UsageEventsTable).Scan(&count); err != nil {
		t.Fatalf("count neuralwatt columns: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 neuralwatt columns after re-migrate, found %d", count)
	}
}

// TestProxyPoolsSchemaAndBindingColumns verifies the proxy_pools table is
// created by EnsureSchema and the binding columns exist on the provider +
// entry tables after Migrate.
func TestProxyPoolsSchemaAndBindingColumns(t *testing.T) {
	pg := newTestPostgresStore(t, "test_proxy_pools_schema")
	defer pg.Close()
	ensureMigrated(t, pg)
	ctx := context.Background()

	var exists bool
	err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = $1 AND table_name = $2)`, pg.cfg.Schema, "proxy_pools").Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("proxy_pools table missing: %v %v", exists, err)
	}
	for _, tc := range []struct{ table, column string }{
		{"upstream_providers", "proxy_pool_id"},
		{"upstream_provider_api_key_entries", "proxy_pool_id"},
	} {
		err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3)`,
			pg.cfg.Schema, tc.table, tc.column).Scan(&exists)
		if err != nil || !exists {
			t.Fatalf("%s.%s missing: %v %v", tc.table, tc.column, exists, err)
		}
	}
	// Default columns materialized with the expected defaults.
	var strictDefault, activeDefault string
	if err := pg.DB().QueryRowContext(ctx, `SELECT column_default FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'proxy_pools' AND column_name = 'strict_proxy'`,
		pg.cfg.Schema).Scan(&strictDefault); err != nil {
		t.Fatalf("strict_proxy default query: %v", err)
	}
	if !strings.Contains(strictDefault, "true") {
		t.Fatalf("strict_proxy default = %q, want true", strictDefault)
	}
	if err := pg.DB().QueryRowContext(ctx, `SELECT column_default FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'proxy_pools' AND column_name = 'is_active'`,
		pg.cfg.Schema).Scan(&activeDefault); err != nil {
		t.Fatalf("is_active default query: %v", err)
	}
	if !strings.Contains(activeDefault, "true") {
		t.Fatalf("is_active default = %q, want true", activeDefault)
	}
}

// TestMigrateAddsUsageErrorClassColumns verifies the error_class and
// error_fingerprint columns are added to usage_errors by Migrate and that
// running Migrate twice is idempotent (no duplicate columns).
func TestMigrateAddsUsageErrorClassColumns(t *testing.T) {
	pg := newTestPostgresStore(t, "test_migrate_error_class_cols")
	defer pg.Close()
	ensureMigrated(t, pg)
	ctx := context.Background()

	for _, col := range []string{"error_class", "error_fingerprint"} {
		var exists bool
		if err := pg.DB().QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
			)`, pg.cfg.Schema, pg.cfg.UsageErrorsTable, col).Scan(&exists); err != nil {
			t.Fatalf("query column %s: %v", col, err)
		}
		if !exists {
			t.Fatalf("column %s missing after Migrate", col)
		}
	}

	// Idempotent: a second run must be a no-op.
	ensureMigrated(t, pg)
	var count int
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		  AND column_name IN ('error_class', 'error_fingerprint')
	`, pg.cfg.Schema, pg.cfg.UsageErrorsTable).Scan(&count); err != nil {
		t.Fatalf("count error columns: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 error_class/fingerprint columns after re-migrate, found %d", count)
	}
}

// TestEnsureSchemaBackfillsUsageErrorClass verifies the one-shot backfill for
// pre-existing rows: rows inserted with a NULL error_class (as an old store
// would have) are classified from fail_status_code alone when EnsureSchema
// runs again, and error_fingerprint is left empty because the raw body is not
// available. The backfill must be idempotent — a second run leaves populated
// rows untouched.
func TestEnsureSchemaBackfillsUsageErrorClass(t *testing.T) {
	pg := newTestPostgresStore(t, "test_backfill_error_class")
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	table := pg.fullTableName(pg.cfg.UsageErrorsTable)
	// Insert legacy-style rows: error_class NULL, fingerprint NULL. provider and
	// model are the only NOT NULL columns besides requested_at.
	statuses := []int{429, 401, 403, 404, 422, 504, 500, 418}
	for _, code := range statuses {
		if _, err := pg.DB().ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (provider, model, fail_status_code, requested_at)
			 VALUES ('openai', 'gpt-4', $1, NOW())`, table), code,
		); err != nil {
			t.Fatalf("insert legacy row (status %d): %v", code, err)
		}
	}

	// Re-run EnsureSchema: the same path a server restart takes.
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema (backfill): %v", err)
	}

	wantByStatus := map[int]string{
		429: "rate_limit",
		401: "auth",
		403: "permission",
		404: "not_found",
		422: "invalid_request",
		504: "timeout",
		500: "server_error",
		418: "other",
	}
	for _, code := range statuses {
		var got string
		var fingerprint *string
		if err := pg.DB().QueryRowContext(ctx, fmt.Sprintf(
			`SELECT error_class, error_fingerprint FROM %s WHERE fail_status_code = $1`, table),
			code,
		).Scan(&got, &fingerprint); err != nil {
			t.Fatalf("select backfilled row (status %d): %v", code, err)
		}
		if got != wantByStatus[code] {
			t.Errorf("status %d: error_class = %q; want %q", code, got, wantByStatus[code])
		}
		if fingerprint != nil {
			t.Errorf("status %d: error_fingerprint = %q; want NULL (body unavailable)", code, *fingerprint)
		}
	}

	// Idempotent: a third run must not change the already-classified rows.
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema (second backfill run): %v", err)
	}
	var nullCount int
	if err := pg.DB().QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE error_class IS NULL`, table),
	).Scan(&nullCount); err != nil {
		t.Fatalf("count NULL error_class: %v", err)
	}
	if nullCount != 0 {
		t.Errorf("%d rows still unclassified after backfill", nullCount)
	}
}
