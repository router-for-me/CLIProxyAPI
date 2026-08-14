package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// newTestImportHandler opens a PG-backed APIKeyStore (plus the UsageStore the
// requirePG guard also demands) and wires them into a bare Handler so requirePG
// passes. Skips when PGSTORE_TEST_DSN is unset. The policy service is left nil
// (InvalidateAll is nil-guarded) so no policy wiring is needed for these
// handler tests.
func newTestImportHandler(t *testing.T, schema string) *Handler {
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
	for _, table := range []string{pg.APIKeysTable(), pg.PoliciesTable(), pg.InternalUsersTable()} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			_ = err
		}
	}
	h := &Handler{}
	h.mu.Lock()
	h.pgAPIKeys = store.NewAPIKeyStore(pg)
	h.pgUsage = store.NewUsageStore(pg)
	h.mu.Unlock()
	return h
}

// TestImportPGAPIKeysRoundTrip seeds keys with known aliases, imports custom
// secrets against them, and verifies the report plus the rotated hash.
func TestImportPGAPIKeysRoundTrip(t *testing.T) {
	h := newTestImportHandler(t, "import_keys")
	apiKeys := h.pgAPIKeys
	ctx := context.Background()

	keyA, _, err := apiKeys.Create(ctx, "key-a", "alpha", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	keyB, _, err := apiKeys.Create(ctx, "key-b", "beta", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if _, _, err := apiKeys.Create(ctx, "dup-1", "dup", "", nil, nil, nil); err != nil {
		t.Fatalf("create dup-1: %v", err)
	}
	if _, _, err := apiKeys.Create(ctx, "dup-2", "dup", "", nil, nil, nil); err != nil {
		t.Fatalf("create dup-2: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// Row 5 (beta) offers the secret ALPHA already owns on row 1: a duplicate
	// secret must be skipped rather than silently shadowing alpha's row.
	body := `{"keys":[
		{"alias":"ALPHA","key":"sk-custom-alpha-secret-0123456789"},
		{"alias":"beta","key":"sk-custom-beta-secret-0123456789"},
		{"alias":"ghost","key":"sk-custom-ghost-secret-0123456789"},
		{"alias":"dup","key":"sk-custom-dup-secret-0123456789"},
		{"alias":"beta","key":"sk-custom-alpha-secret-0123456789"},
		{"alias":"","key":"sk-custom-emptyalias-0123456789"},
		{"alias":"beta","key":"short"}
	]}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-keys-pg/import", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.ImportPGAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp pgImportKeysResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 7 {
		t.Errorf("total = %d; want 7", resp.Total)
	}
	if resp.Imported != 2 {
		t.Errorf("imported = %d; want 2 (ALPHA→alpha, beta→beta)", resp.Imported)
	}

	// The alpha key now carries the custom secret hash; beta too.
	alphaHash := store.HashSecret("sk-custom-alpha-secret-0123456789")
	betaHash := store.HashSecret("sk-custom-beta-secret-0123456789")
	alphaByHash, _, err := apiKeys.LookupByHash(ctx, alphaHash)
	if err != nil {
		t.Errorf("alpha custom secret not active: %v", err)
	} else if alphaByHash.ID != keyA.ID {
		t.Errorf("alpha hash resolves to %s; want %s", alphaByHash.ID, keyA.ID)
	}
	betaByHash, _, err := apiKeys.LookupByHash(ctx, betaHash)
	if err != nil {
		t.Errorf("beta custom secret not active: %v", err)
	} else if betaByHash.ID != keyB.ID {
		t.Errorf("beta hash resolves to %s; want %s", betaByHash.ID, keyB.ID)
	}
	// The duplicate-secret row (beta + alpha's secret) must NOT have been
	// applied: beta still owns the beta secret, and its ID is unchanged.
	if _, _, err := apiKeys.LookupByID(ctx, keyA.ID); err != nil {
		t.Errorf("keyA lookup after import: %v", err)
	}
	if _, _, err := apiKeys.LookupByID(ctx, keyB.ID); err != nil {
		t.Errorf("keyB lookup after import: %v", err)
	}
}
