package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// backupTestSchema returns a schema name unique enough that concurrent or
// repeated runs never collide.
func backupTestSchema(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// newTestBackupHandler opens a PG-backed store, wires it into a bare Handler via
// SetBackupStore, and returns the handler plus a router serving the /export and
// /import routes. Skips when PGSTORE_TEST_DSN is unset.
func newTestBackupHandler(t *testing.T, prefix string) (*Handler, *gin.Engine) {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{DSN: pgTestDSN(), Schema: backupTestSchema(prefix)})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Clean the api_keys + policies tables so the export dump is deterministic.
	for _, table := range []string{pg.APIKeysTable(), pg.PoliciesTable()} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	h := newBareHandler()
	h.SetBackupStore(pg)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/export", h.ExportAllData)
	g.POST("/import", h.ImportAllData)
	return h, r
}

func TestParseBackupResources(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []store.BackupResource
		wantErr string
	}{
		{name: "empty", input: "", want: nil},
		{name: "whitespace only", input: "  , ,  ", want: nil},
		{name: "single", input: "api_keys", want: []store.BackupResource{store.ResourceAPIKeys}},
		{name: "multiple", input: "api_keys,usage,sync_log", want: []store.BackupResource{
			store.ResourceAPIKeys, store.ResourceUsage, store.ResourceSyncLog,
		}},
		{name: "whitespace and dedupe", input: " api_keys , api_keys , usage ", want: []store.BackupResource{
			store.ResourceAPIKeys, store.ResourceUsage,
		}},
		{name: "unknown", input: "api_keys,nonsense", wantErr: "unknown resource: nonsense"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, errMsg := parseBackupResources(tc.input)
			if tc.wantErr != "" {
				if errMsg == "" {
					t.Fatalf("expected error %q, got none", tc.wantErr)
				}
				if errMsg != tc.wantErr {
					t.Fatalf("error = %q, want %q", errMsg, tc.wantErr)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected error %q", errMsg)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllBackupResourcesValid(t *testing.T) {
	for _, r := range store.AllBackupResources {
		if !store.ValidBackupResource(string(r)) {
			t.Fatalf("AllBackupResources contains %q but ValidBackupResource says invalid", r)
		}
	}
	if !store.ValidBackupResource("api_keys") {
		t.Fatal("api_keys should be a valid resource")
	}
	if store.ValidBackupResource("does-not-exist") {
		t.Fatal("does-not-exist should not be a valid resource")
	}
}

// TestExportHandlersStreaming verifies the /export handler returns a parseable
// JSON bundle without ?download=1, and that ?download=1 additionally streams the
// same document with a Content-Disposition attachment header and the streamed
// branch's exact application/json content type.
func TestExportHandlersStreaming(t *testing.T) {
	_, r := newTestBackupHandler(t, "backup_export")

	// Non-download: in-memory JSON bundle carrying the resources key.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/export?resources=api_keys", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("non-download status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("non-download Content-Type = %q; want it to contain application/json", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("non-download Content-Disposition = %q; want empty", cd)
	}
	var bundle struct {
		Resources map[string]json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("non-download body is not valid JSON: %v", err)
	}
	if _, ok := bundle.Resources["api_keys"]; !ok {
		t.Errorf("non-download bundle missing api_keys resource; resources=%v", bundle.Resources)
	}

	// Download: streamed branch sets Content-Disposition + exact content type.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/export?resources=api_keys&download=1", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "nixllm_export") {
		t.Errorf("download Content-Disposition = %q; want it to contain nixllm_export", cd)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("download Content-Type = %q; want exact application/json (streamed branch)", ct)
	}
	var streamed struct {
		Resources map[string]json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &streamed); err != nil {
		t.Fatalf("download body is not valid JSON: %v", err)
	}
	if _, ok := streamed.Resources["api_keys"]; !ok {
		t.Errorf("download bundle missing api_keys resource; resources=%v", streamed.Resources)
	}
}

// TestExportDownloadClearsDispositionOnPreWriteError verifies that when the
// streamed export fails before any bytes are flushed, the 500 error response
// drops the Content-Disposition header so a download-style client does not save
// the error body as an attachment file instead of surfacing the 500.
func TestExportDownloadClearsDispositionOnPreWriteError(t *testing.T) {
	h, r := newTestBackupHandler(t, "backup_export_err")
	// Close the underlying DB so the stream's pre-write COUNT pass fails before
	// anything has been flushed to the response.
	if err := h.pgBackup.DB().Close(); err != nil {
		t.Fatalf("close test db: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/export?resources=api_keys&download=1", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500; body=%s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("500 response Content-Disposition = %q; want empty (error must not be saved as a file)", cd)
	}
}

// TestImportAllDataSurfacesPartial verifies the /import handler propagates the
// store report's partial flag and per-resource soft error into the JSON response
// when a chunked data resource skips rows (continue-to-end soft failure).
func TestImportAllDataSurfacesPartial(t *testing.T) {
	h, r := newTestBackupHandler(t, "backup_import")

	// A usage_events row with a non-integer id fails its whole multi-row INSERT
	// chunk, so the resource is skipped and the import is flagged partial.
	badRow := `{"id":"not-an-int","request_id":"req-1","api_key_principal":"sk-test","provider":"anthropic","model":"claude-opus","input_tokens":10,"output_tokens":5,"reasoning_tokens":0,"cached_tokens":0,"cache_creation_tokens":0,"total_tokens":15,"cost_usd":0.001,"failed":false,"generate":false,"requested_at":"2026-08-15T00:00:00Z","flushed_at":"2026-08-15T00:00:00Z","discount_pct":0,"original_cost_usd":0.001}`
	bundle := map[string]any{
		"version":     1,
		"exported_at": "2026-08-15T00:00:00Z",
		"resources": map[string]any{
			"usage": map[string]any{
				"tables": map[string]any{
					h.pgBackup.UsageEventsTable(): []string{badRow},
				},
			},
		},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/import?resources=usage", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d; want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Import struct {
			Partial   bool `json:"partial"`
			Resources map[string]struct {
				Inserted int    `json:"inserted"`
				Skipped  int    `json:"skipped"`
				Error    string `json:"error"`
			} `json:"resources"`
		} `json:"import"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if !resp.Import.Partial {
		t.Error("import.partial = false; want true when a chunk is skipped")
	}
	ur, ok := resp.Import.Resources["usage"]
	if !ok {
		t.Fatalf("response missing usage resource report; got %v", resp.Import.Resources)
	}
	if ur.Skipped == 0 {
		t.Errorf("usage skipped = %d; want > 0", ur.Skipped)
	}
	if ur.Error == "" {
		t.Error("usage error is empty; want a soft-failure message")
	}
}
