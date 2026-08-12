package store

import (
	"context"
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

// TestMigrateIdempotent is currently a light no-op check: in Task 2 Migrate was
// a no-op, so running it twice only exercises the initialized guard. The real
// DDL idempotency assertions (the usage_events indexes) live in
// TestMigrateCreatesUsageIndexes.
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
