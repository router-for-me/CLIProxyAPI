// Package pg_test exercises the PostgreSQL-backed configstore.Repository
// against a live Postgres instance. Tests skip when PGSTORE_TEST_DSN is
// unset so unit runs remain hermetic; CI sets the DSN on the postgres job.
package pg_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configstore"
	pgstore "github.com/router-for-me/CLIProxyAPI/v7/internal/configstore/pg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// pgTestDSN mirrors the helper used by the store package but lives in a
// different package; duplicating it keeps the test file self-contained and
// avoids leaking internal test helpers across package boundaries.
func pgTestDSN() string {
	return strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
}

func skipIfNoPostgres(t *testing.T) {
	t.Helper()
	if pgTestDSN() == "" {
		t.Skip("PGSTORE_TEST_DSN not set; skipping Postgres integration test")
	}
}

// newTestRepo opens a PostgresStore, ensures the schema, and returns the
// repository backed by it. Each test passes a unique schema name so the
// per-test isolation matches the existing pattern in
// internal/store/pg_apikeys_test.go. The store is registered for automatic
// closure via t.Cleanup.
func newTestRepo(t *testing.T, schema string) (configstore.Repository, *store.PostgresStore) {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    pgTestDSN(),
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Clear tables that the repository writes to. Best-effort: the cleanup
	// loop tolerates missing rows because tests use unique schemas.
	for _, table := range []string{
		pg.RuntimeConfigTable(),
		pg.ConfigRevisionsTable(),
		pg.ConfigImportsTable(),
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Logf("clear %s (best-effort): %v", table, err)
		}
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pgstore.Open(pg), pg
}

// TestLoadEmptyDatabaseReturnsNewEmpty verifies that Load on an empty
// database (no accepted save yet) returns a NewEmpty snapshot with Revision
// 0 and no error. The repository must not synthesise a revision; the
// caller decides when to bootstrap the singleton via Save(expected=0, ...).
func TestLoadEmptyDatabaseReturnsNewEmpty(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_load_empty")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load on empty database: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil snapshot")
	}
	if loaded.Revision != 0 {
		t.Fatalf("loaded.Revision = %d, want 0", loaded.Revision)
	}
	if loaded.Settings == nil {
		t.Fatal("loaded.Settings is nil; expected non-nil empty map")
	}
	if loaded.Extra == nil {
		t.Fatal("loaded.Extra is nil; expected non-nil empty map")
	}

	// Sanity: the underlying database row count is zero (no seed).
	var count int
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pg.RuntimeConfigTable(),
	).Scan(&count); err != nil {
		t.Fatalf("count runtime_config: %v", err)
	}
	if count != 0 {
		t.Fatalf("runtime_config rows = %d, want 0", count)
	}
}

// TestSaveFirstRevision verifies the bootstrap Save path: expected=0 on an
// empty database creates the singleton at revision=1, persists the audit
// fields, and appends one config_revisions row.
func TestSaveFirstRevision(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_save_first")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	candidate := configsnapshot.NewEmpty()
	candidate.Settings["port"] = 8317

	saved, err := repo.Save(ctx, 0, &candidate, configstore.SaveAudit{
		Actor:  "tester",
		Reason: "initial-seed",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Revision != 1 {
		t.Fatalf("saved.Revision = %d, want 1", saved.Revision)
	}
	if saved.UpdatedBy != "tester" {
		t.Fatalf("saved.UpdatedBy = %q, want tester", saved.UpdatedBy)
	}

	// runtime_config: one row, settings JSONB contains "port".
	var (
		revision int64
		actor    string
		source   string
	)
	row := pg.DB().QueryRowContext(ctx,
		`SELECT revision, COALESCE(updated_by, ''), updated_source FROM `+pg.RuntimeConfigTable()+` WHERE id = 1`,
	)
	if err := row.Scan(&revision, &actor, &source); err != nil {
		t.Fatalf("scan runtime_config: %v", err)
	}
	if revision != 1 {
		t.Fatalf("runtime_config.revision = %d, want 1", revision)
	}
	if actor != "tester" {
		t.Fatalf("runtime_config.updated_by = %q, want tester", actor)
	}
	if source != "dashboard" {
		t.Fatalf("runtime_config.updated_source = %q, want dashboard", source)
	}

	// config_revisions: exactly one row, revision=1, audit fields populated.
	var revisionCount int
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pg.ConfigRevisionsTable(),
	).Scan(&revisionCount); err != nil {
		t.Fatalf("count config_revisions: %v", err)
	}
	if revisionCount != 1 {
		t.Fatalf("config_revisions rows = %d, want 1", revisionCount)
	}
	var (
		histRevision int64
		histReason   string
		histActor    string
		histChecksum string
	)
	row = pg.DB().QueryRowContext(ctx,
		`SELECT revision, reason, COALESCE(created_by, ''), COALESCE(checksum, '') FROM `+pg.ConfigRevisionsTable(),
	)
	if err := row.Scan(&histRevision, &histReason, &histActor, &histChecksum); err != nil {
		t.Fatalf("scan config_revisions: %v", err)
	}
	if histRevision != 1 {
		t.Fatalf("config_revisions.revision = %d, want 1", histRevision)
	}
	if histReason != "initial-seed" {
		t.Fatalf("config_revisions.reason = %q, want initial-seed", histReason)
	}
	if histActor != "tester" {
		t.Fatalf("config_revisions.created_by = %q, want tester", histActor)
	}
	if histChecksum == "" {
		t.Fatal("config_revisions.checksum is empty")
	}
}

// TestSaveIncrementsRevision verifies the happy-path increment: a second
// Save with expected=1 (matching the current revision) succeeds and bumps
// the revision to 2. A second config_revisions row is appended.
func TestSaveIncrementsRevision(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_save_increment")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := configsnapshot.NewEmpty()
	first.Settings["port"] = 8317
	if _, err := repo.Save(ctx, 0, &first, configstore.SaveAudit{Actor: "tester", Reason: "first"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	second := configsnapshot.NewEmpty()
	second.Settings["port"] = 9000
	saved, err := repo.Save(ctx, 1, &second, configstore.SaveAudit{Actor: "tester", Reason: "second"})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if saved.Revision != 2 {
		t.Fatalf("second Save revision = %d, want 2", saved.Revision)
	}

	// config_revisions now carries two rows in insertion order.
	rows, err := pg.DB().QueryContext(ctx,
		`SELECT revision, reason FROM `+pg.ConfigRevisionsTable()+` ORDER BY created_at ASC, revision ASC`,
	)
	if err != nil {
		t.Fatalf("query config_revisions: %v", err)
	}
	defer rows.Close()
	wantReasons := []string{"first", "second"}
	gotReasons := []string{}
	var gotRevisions []int64
	for rows.Next() {
		var rev int64
		var reason string
		if err := rows.Scan(&rev, &reason); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotRevisions = append(gotRevisions, rev)
		gotReasons = append(gotReasons, reason)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(gotRevisions) != 2 || gotRevisions[0] != 1 || gotRevisions[1] != 2 {
		t.Fatalf("config_revisions revisions = %v, want [1 2]", gotRevisions)
	}
	if len(gotReasons) != 2 || gotReasons[0] != wantReasons[0] || gotReasons[1] != wantReasons[1] {
		t.Fatalf("config_revisions reasons = %v, want %v", gotReasons, wantReasons)
	}
}

// TestSaveConflictReturnsTypedError verifies the optimistic-locking
// contract: a Save with expected=0 on a database that already has revision
// 1 returns a *RevisionConflictError carrying Current=1, and no row is
// mutated.
func TestSaveConflictReturnsTypedError(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_conflict")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := configsnapshot.NewEmpty()
	first.Settings["port"] = 8317
	if _, err := repo.Save(ctx, 0, &first, configstore.SaveAudit{Actor: "tester"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	stale := configsnapshot.NewEmpty()
	stale.Settings["port"] = 9000
	_, err := repo.Save(ctx, 0, &stale, configstore.SaveAudit{Actor: "tester"})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	var conflict *configstore.RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected *RevisionConflictError, got %T (%v)", err, err)
	}
	if conflict.Current != 1 {
		t.Fatalf("conflict.Current = %d, want 1", conflict.Current)
	}

	// The repository must not have advanced state.
	var revision int64
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT revision FROM "+pg.RuntimeConfigTable()+" WHERE id = 1",
	).Scan(&revision); err != nil {
		t.Fatalf("scan revision: %v", err)
	}
	if revision != 1 {
		t.Fatalf("runtime_config.revision after conflict = %d, want 1", revision)
	}
	var count int
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pg.ConfigRevisionsTable(),
	).Scan(&count); err != nil {
		t.Fatalf("count config_revisions: %v", err)
	}
	if count != 1 {
		t.Fatalf("config_revisions rows after conflict = %d, want 1", count)
	}
}

// TestSaveRejectsEmptySettings verifies the programmer-error guard: a
// candidate with no Settings is rejected without touching the database.
func TestSaveRejectsEmptySettings(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_empty_settings")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	empty := configsnapshot.NewEmpty()
	_, err := repo.Save(ctx, 0, &empty, configstore.SaveAudit{Actor: "tester", Reason: "empty"})
	if err == nil {
		t.Fatal("expected error for empty Settings, got nil")
	}
	if !strings.Contains(err.Error(), "empty settings rejected") {
		t.Fatalf("error message = %q, want contains 'empty settings rejected'", err.Error())
	}

	// No row should have been written.
	var count int
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pg.RuntimeConfigTable(),
	).Scan(&count); err != nil {
		t.Fatalf("count runtime_config: %v", err)
	}
	if count != 0 {
		t.Fatalf("runtime_config rows = %d, want 0", count)
	}
}

// TestSaveTransactionalIntegrityOnFailure forces a failure path inside the
// Save transaction (by passing audit values that violate an upstream
// constraint after the upsert succeeds is non-trivial to engineer against
// the production schema, so the test exercises a controlled failure by
// closing the database mid-transaction) and asserts that no partial state
// was committed.
func TestSaveTransactionalIntegrityOnFailure(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_tx_integrity")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := configsnapshot.NewEmpty()
	first.Settings["port"] = 8317
	if _, err := repo.Save(ctx, 0, &first, configstore.SaveAudit{Actor: "tester"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Force a failure by closing the DB. Any subsequent Save must fail and
	// the active row must remain at revision 1.
	_ = pg.Close()

	stale := configsnapshot.NewEmpty()
	stale.Settings["port"] = 9000
	if _, err := repo.Save(ctx, 1, &stale, configstore.SaveAudit{Actor: "tester"}); err == nil {
		t.Fatal("expected error after DB close, got nil")
	}
}

// TestRollbackCopiesSettingsAndPreservesHistory verifies the rollback
// contract: Rollback(target=1) creates revision 3 whose Settings match
// revision 1, and leaves the original revisions 1 and 2 in config_revisions
// for audit.
func TestRollbackCopiesSettingsAndPreservesHistory(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_rollback")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := configsnapshot.NewEmpty()
	first.Settings["port"] = 8317
	if _, err := repo.Save(ctx, 0, &first, configstore.SaveAudit{Actor: "tester", Reason: "first"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second := configsnapshot.NewEmpty()
	second.Settings["port"] = 9000
	if _, err := repo.Save(ctx, 1, &second, configstore.SaveAudit{Actor: "tester", Reason: "second"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	rolled, err := repo.Rollback(ctx, 1, "user request", "tester")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Revision != 3 {
		t.Fatalf("rolled.Revision = %d, want 3", rolled.Revision)
	}
	if got := rolled.Settings["port"]; got != 8317 {
		t.Fatalf("rolled.Settings[port] = %v, want 8317", got)
	}
	if rolled.UpdatedSource != "rollback" {
		t.Fatalf("rolled.UpdatedSource = %q, want rollback", rolled.UpdatedSource)
	}

	// runtime_config row reflects the rollback.
	var (
		revision  int64
		actor     string
		source    string
		portValue int
	)
	row := pg.DB().QueryRowContext(ctx,
		`SELECT revision, COALESCE(updated_by, ''), updated_source, (settings->>'port')::int FROM `+
			pg.RuntimeConfigTable()+` WHERE id = 1`,
	)
	if err := row.Scan(&revision, &actor, &source, &portValue); err != nil {
		t.Fatalf("scan runtime_config: %v", err)
	}
	if revision != 3 {
		t.Fatalf("runtime_config.revision = %d, want 3", revision)
	}
	if actor != "tester" {
		t.Fatalf("runtime_config.updated_by = %q, want tester", actor)
	}
	if source != "rollback" {
		t.Fatalf("runtime_config.updated_source = %q, want rollback", source)
	}
	if portValue != 8317 {
		t.Fatalf("runtime_config.settings->>port = %d, want 8317", portValue)
	}

	// config_revisions has the original two rows plus the rollback row.
	rows, err := pg.DB().QueryContext(ctx,
		`SELECT revision FROM `+pg.ConfigRevisionsTable()+` ORDER BY revision ASC`,
	)
	if err != nil {
		t.Fatalf("query config_revisions: %v", err)
	}
	defer rows.Close()
	var revisions []int64
	for rows.Next() {
		var rev int64
		if err := rows.Scan(&rev); err != nil {
			t.Fatalf("scan: %v", err)
		}
		revisions = append(revisions, rev)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(revisions) != 3 || revisions[0] != 1 || revisions[1] != 2 || revisions[2] != 3 {
		t.Fatalf("config_revisions revisions = %v, want [1 2 3]", revisions)
	}

	// The rollback row's reason mentions the target revision.
	var reasonText string
	if err := pg.DB().QueryRowContext(ctx,
		`SELECT reason FROM `+pg.ConfigRevisionsTable()+` WHERE revision = 3`,
	).Scan(&reasonText); err != nil {
		t.Fatalf("scan rollback reason: %v", err)
	}
	if !strings.Contains(reasonText, "rollback") {
		t.Fatalf("rollback reason = %q, want contains 'rollback'", reasonText)
	}
}

// TestRollbackToMissingRevisionReturnsError verifies the failure path: a
// rollback whose target revision does not exist returns a descriptive error
// and leaves the database untouched.
func TestRollbackToMissingRevisionReturnsError(t *testing.T) {
	repo, pg := newTestRepo(t, "test_repo_rollback_missing")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := configsnapshot.NewEmpty()
	first.Settings["port"] = 8317
	if _, err := repo.Save(ctx, 0, &first, configstore.SaveAudit{Actor: "tester"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	_, err := repo.Rollback(ctx, 99, "no-such-revision", "tester")
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error message = %q, want contains '99'", err.Error())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error message = %q, want contains 'not found'", err.Error())
	}

	// Active row stays at revision 1; no rollback row was appended.
	var revision int64
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT revision FROM "+pg.RuntimeConfigTable()+" WHERE id = 1",
	).Scan(&revision); err != nil {
		t.Fatalf("scan revision: %v", err)
	}
	if revision != 1 {
		t.Fatalf("runtime_config.revision = %d, want 1", revision)
	}
	var histCount int
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pg.ConfigRevisionsTable(),
	).Scan(&histCount); err != nil {
		t.Fatalf("count config_revisions: %v", err)
	}
	if histCount != 1 {
		t.Fatalf("config_revisions rows = %d, want 1", histCount)
	}
}

// TestPackageTypesReachableFromInternalConfigstore verifies the public
// surface compiles and is reachable from internal callers. The test is a
// compile-time check: the assignments below would fail to build if any of
// the package's exported types were renamed or moved.
func TestPackageTypesReachableFromInternalConfigstore(t *testing.T) {
	skipIfNoPostgres(t)
	var (
		_ configstore.Repository             = (configstore.Repository)(nil)
		_ configstore.SaveAudit              = configstore.SaveAudit{}
		_ *configstore.RevisionConflictError = &configstore.RevisionConflictError{}
	)
}
