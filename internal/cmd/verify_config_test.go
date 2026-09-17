package cmd

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// cmdTestDSN returns the connection string to a real Postgres instance for
// integration tests. When unset, the PG-backed tests skip so unit runs
// remain hermetic.
func cmdTestDSN() string {
	return strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
}

func cmdSkipIfNoPostgres(t *testing.T) {
	t.Helper()
	if cmdTestDSN() == "" {
		t.Skip("PGSTORE_TEST_DSN not set; skipping Postgres integration test")
	}
}

// newCmdTestPG opens a PostgresStore against PGSTORE_TEST_DSN with a unique
// schema per test so parallel runs do not collide. The store is closed via
// t.Cleanup. Tests use a separate schema per case, so the helper only
// ensures schema materialization — it does not pre-truncate the runtime
// tables because each test owns its schema.
func newCmdTestPG(t *testing.T, schema string) *store.PostgresStore {
	t.Helper()
	cmdSkipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    cmdTestDSN(),
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	// Force-clean any rows a previous run with the same schema name left
	// behind so a fresh run is not blocked by a revision conflict.
	clearCtx, clearCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer clearCancel()
	for _, table := range []string{
		pg.RuntimeConfigTable(),
		pg.ConfigRevisionsTable(),
		pg.ConfigImportsTable(),
		pg.APIKeysTable(),
		pg.UpstreamProvidersTable(),
	} {
		if _, err := pg.DB().ExecContext(clearCtx, "DELETE FROM "+table); err != nil {
			t.Logf("clear %s (best-effort): %v", table, err)
		}
	}
	return pg
}

// TestVerifyConfigAfterSeedBootAutoImport drives the same DoImportConfig
// helper the boot path uses so the verify-config checks fire on a real
// (non-empty) runtime_config singleton. DoImportConfig runs in a separate
// connection because the test helper holds one open for cleanup.
func TestVerifyConfigAfterSeedBootAutoImport(t *testing.T) {
	schema := "test_verify_after_seed"
	newCmdTestPG(t, schema)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seed := "../../config.example.yaml"
	if _, err := DoImportConfig(ctx, ImportConfigOptions{
		SourcePath: seed,
		Schema:     schema,
		DSN:        cmdTestDSN(),
	}); err != nil {
		t.Fatalf("seed DoImportConfig: %v", err)
	}

	if err := DoVerifyConfig(ctx, VerifyConfigOptions{Schema: schema, DSN: cmdTestDSN()}); err != nil {
		t.Fatalf("DoVerifyConfig: %v", err)
	}
}

// TestVerifyConfigEmptyDatabase asserts the command fails closed when no
// runtime_config row exists yet.
func TestVerifyConfigEmptyDatabase(t *testing.T) {
	schema := "test_verify_empty"
	newCmdTestPG(t, schema)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := DoVerifyConfig(ctx, VerifyConfigOptions{Schema: schema, DSN: cmdTestDSN()}); err == nil {
		t.Fatal("expected error on empty runtime_config, got nil")
	}
}

// TestVerifyConfigRequiresDSN asserts the command rejects missing DSN
// before touching the database.
func TestVerifyConfigRequiresDSN(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "")
	if err := DoVerifyConfig(context.Background(), VerifyConfigOptions{Schema: "public"}); err == nil {
		t.Fatal("expected error when PGSTORE_DSN is missing, got nil")
	}
}
