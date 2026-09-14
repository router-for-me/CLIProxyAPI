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
