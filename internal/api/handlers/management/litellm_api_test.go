package management

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestRequireLiteLLMRuntimeUnwired verifies the compat-route guard reports
// "unwired" (writes a 503 pg_store_not_configured response and returns false)
// when the runtime PG stores backing the /litellm routes are absent.
func TestRequireLiteLLMRuntimeUnwired(t *testing.T) {
	h := &Handler{} // nil stores
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/litellm/guard", nil)
	if users, keys, usage, ok := h.requireLiteLLMRuntime(c); ok {
		t.Fatalf("expected requireLiteLLMRuntime to report unwired, got stores users=%v keys=%v usage=%v", users, keys, usage)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", rec.Code)
	}
}

// newTestLiteLLMCompatHandler opens a PG-backed UserStore/APIKeyStore/UsageStore
// against PGSTORE_TEST_DSN, cleans the managed tables, and wires them into a
// Handler so the requireLiteLLMRuntime guard passes. Skips when PG is unset.
func newTestLiteLLMCompatHandler(t *testing.T, schema string) *Handler {
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
		pg.InternalUsersTable(),
		pg.APIKeysTable(),
		pg.PoliciesTable(),
		pg.UsageEventsTable(),
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			_ = err
		}
	}
	h := &Handler{}
	h.mu.Lock()
	h.pgUsers = store.NewUserStore(pg)
	h.pgAPIKeys = store.NewAPIKeyStore(pg)
	h.pgUsage = store.NewUsageStore(pg)
	h.mu.Unlock()
	return h
}

// TestCreateLiteLLMUserCompatRoundTrip verifies POST /litellm/user/new persists
// an internal user and responds 201 with LiteLLM field names, not NixLLM's
// internal ID/UserAlias casing.
func TestCreateLiteLLMUserCompatRoundTrip(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_new")
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"user_id":"team-a","user_alias":"Team A","user_email":"a@example.com","models":["gpt-4o"],"max_budget":10}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/user/new", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.CreateLiteLLMUserCompat(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()
	for _, want := range []string{
		`"user_id":"team-a"`,
		`"user_alias":"Team A"`,
		`"user_email":"a@example.com"`,
		`"models"`,
		`"max_budget"`,
		`"spend"`,
		`"created_at"`,
		`"updated_at"`,
	} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("response missing %s; body=%s", want, resp)
		}
	}
	// The internal NixLLM casing must NOT leak into the compat response.
	if bytes.Contains(rec.Body.Bytes(), []byte(`"UserAlias"`)) {
		t.Errorf("response leaked internal casing; body=%s", resp)
	}
}
