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
	// The handler-facing helper must surface the same counts from the header.
	if got := bundle.ResourceRowCounts()["api_keys"]; got != 2 {
		t.Fatalf("ResourceRowCounts api_keys = %d, want 2", got)
	}
	if got := bundle.ResourceRowCounts()["internal_users"]; got != 1 {
		t.Fatalf("ResourceRowCounts internal_users = %d, want 1", got)
	}
}

// TestBackupResourceRowCountsLegacyFallback pins the compatibility contract for
// bundles exported before the header summary existed: when Summary is absent,
// ResourceRowCounts must derive the per-resource counts from the exported rows
// themselves.
func TestBackupResourceRowCountsLegacyFallback(t *testing.T) {
	st := roundTripTestStore(t, "backup_sum_legacy_"+randSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seedBackupTestData(t, st)

	bundle, err := st.ExportData(ctx, BackupExportOpts{})
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	// Simulate a legacy bundle by round-tripping through JSON and dropping the
	// header summary entirely.
	enc, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	var legacy BackupBundle
	if err := json.Unmarshal(enc, &legacy); err != nil {
		t.Fatalf("unmarshal bundle: %v", err)
	}
	legacy.Summary = nil

	counts := legacy.ResourceRowCounts()
	if got := counts["api_keys"]; got != 2 { // 1 api key + 1 policy row
		t.Fatalf("legacy fallback api_keys = %d, want 2", got)
	}
	if got := counts["internal_users"]; got != 1 {
		t.Fatalf("legacy fallback internal_users = %d, want 1", got)
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

func randSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
