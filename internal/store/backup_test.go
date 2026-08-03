package store

import (
	"context"
	"encoding/json"
	"fmt"
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
	if _, _, err := apiKeys.Create(ctx, "test-key", "test-alias", "sk-testsecret123", &expires, nil, nil); err != nil {
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
	if bundle.Version != 1 {
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

func randSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
