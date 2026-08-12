package store

import (
	"context"
	"testing"
)

// ensureMigrated runs every registered idempotent migration for the schema
// returned by NewPostgresStore. Tests that rely on tables/indexes added via
// migrations must call it after constructing the store.
func ensureMigrated(t *testing.T, pg *PostgresStore) {
	t.Helper()
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	pg := newTestPostgresStore(t, "test_migrate")
	defer pg.Close()
	ensureMigrated(t, pg)
	// Running Migrate a second time must be a no-op (idempotent).
	ensureMigrated(t, pg)
}
