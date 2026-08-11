package management

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// The management package unit tests are hermetic; the PG-backed tests below
// require a live Postgres reachable via PGSTORE_TEST_DSN and are skipped
// otherwise, mirroring the store package's convention.

func pgTestDSN() string {
	return strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
}

func skipIfNoPostgres(t *testing.T) {
	t.Helper()
	if pgTestDSN() == "" {
		t.Skip("PGSTORE_TEST_DSN not set; skipping Postgres integration test")
	}
}

// newTestSyncStores opens a PostgresStore against PGSTORE_TEST_DSN, cleans the
// managed tables, and returns the runtime APIKeyStore + Manage-LiteLLM key
// store wired to the same connection, mirroring newTestKeyImportStore in the
// store package.
func newTestSyncStores(t *testing.T, schema string) (*store.APIKeyStore, *store.LiteLLMKeyStore) {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{DSN: pgTestDSN(), Schema: schema})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	for _, table := range []string{
		pg.APIKeysTable(),
		pg.PoliciesTable(),
		pg.InternalUsersTable(),
		pg.LiteLLMKeyPoliciesTable(),
		pg.LiteLLMKeysTable(),
		pg.LiteLLMUsersTable(),
		pg.UsageEventsTable(),
		pg.UsageErrorsTable(),
		pg.LiteLLMSyncSettingsTable(),
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			_ = err
		}
	}
	return store.NewAPIKeyStore(pg), store.NewLiteLLMKeyStore(pg)
}

func TestRemoteSpendLogsToEventsMigratesLitellmKey(t *testing.T) {
	runtimeKeys, litellmKeys := newTestSyncStores(t, "mgmt_log_events_migrate")
	ctx := context.Background()

	// A key exists only in the Manage-LiteLLM store (with an alias), not in the
	// runtime api_keys table — the exact scenario that made the alias show "-".
	lk, _, err := litellmKeys.Create(ctx, "prod-key", "team-alias", "sk-any-key-hash-abc", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("litellmKeys.Create: %v", err)
	}
	hash := lk.KeyHash

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	logs := []remoteSpendLog{
		{
			Event: store.UsageEvent{
				RequestID: "req-1", Provider: "litellm", Model: "gpt-4o",
				LatencyMs: 120, TTFTMs: 40, FailStatusCode: 200, Failed: false,
				RequestedAt: base,
			},
			KeyHash: hash,
		},
	}

	evs := (&Handler{}).remoteSpendLogsToEvents(ctx, logs, runtimeKeys, litellmKeys)
	if len(evs) != 1 {
		t.Fatalf("len = %d; want 1", len(evs))
	}
	if evs[0].APIKeyID != lk.ID {
		t.Fatalf("event api_key_id = %q; want %q", evs[0].APIKeyID, lk.ID)
	}

	// The key must now exist in the runtime table with its alias preserved, so
	// the read-path key_alias JOIN resolves to the alias instead of "-".
	got, _, err := runtimeKeys.LookupByHash(ctx, hash)
	if err != nil {
		t.Fatalf("LookupByHash after migration: %v", err)
	}
	if got.ID != lk.ID {
		t.Fatalf("runtime key id = %q; want %q", got.ID, lk.ID)
	}
	if got.KeyAlias != "team-alias" {
		t.Fatalf("runtime key alias = %q; want %q", got.KeyAlias, "team-alias")
	}
}

func TestRemoteSpendLogsToErrorsMigratesLitellmKey(t *testing.T) {
	runtimeKeys, litellmKeys := newTestSyncStores(t, "mgmt_log_errors_migrate")
	ctx := context.Background()

	lk, _, err := litellmKeys.Create(ctx, "prod-key", "team-alias", "sk-any-key-hash-def", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("litellmKeys.Create: %v", err)
	}
	hash := lk.KeyHash

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	logs := []remoteSpendLog{
		{
			Event: store.UsageEvent{
				RequestID: "req-fail", Provider: "litellm", Model: "gpt-4o",
				LatencyMs: 999, TTFTMs: 300, FailStatusCode: 500, Failed: true,
				TotalTokens: 50, RequestedAt: base,
			},
			KeyHash:  hash,
			ErrorMsg: "upstream timeout",
		},
	}

	errs := (&Handler{}).remoteSpendLogsToErrors(ctx, logs, runtimeKeys, litellmKeys)
	if len(errs) != 1 {
		t.Fatalf("len = %d; want 1", len(errs))
	}
	if errs[0].APIKeyID != lk.ID {
		t.Fatalf("error api_key_id = %q; want %q", errs[0].APIKeyID, lk.ID)
	}
	got, _, err := runtimeKeys.LookupByHash(ctx, hash)
	if err != nil {
		t.Fatalf("LookupByHash after migration: %v", err)
	}
	if got.KeyAlias != "team-alias" {
		t.Fatalf("runtime key alias = %q; want %q", got.KeyAlias, "team-alias")
	}
}
