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
	// user_email was set in the request, so it must be present as a value (not
	// omitted): confirm it carries the value.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"user_email":"a@example.com"`)) {
		t.Errorf("response missing user_email value; body=%s", resp)
	}
	// Fields NOT set in the request must be ABSENT (omitempty parity), not
	// present-as-empty. budget_duration / tpm_limit / rpm_limit / metadata were
	// not sent, so they must not appear at all.
	for _, absent := range []string{
		`"budget_duration"`,
		`"tpm_limit"`,
		`"rpm_limit"`,
		`"metadata"`,
	} {
		if bytes.Contains(rec.Body.Bytes(), []byte(absent)) {
			t.Errorf("response contains unset field %s; want omitted; body=%s", absent, resp)
		}
	}
	// The internal NixLLM casing must NOT leak into the compat response.
	if bytes.Contains(rec.Body.Bytes(), []byte(`"UserAlias"`)) {
		t.Errorf("response leaked internal casing; body=%s", resp)
	}
}

// TestListLiteLLMUsersCompat seeds two users and verifies GET /litellm/user/list
// returns them with LiteLLM field names plus pagination fields.
func TestListLiteLLMUsersCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_list")
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	for _, u := range []store.InternalUser{
		{ID: "team-a", UserAlias: "Team A", UserEmail: "a@example.com", UserRole: "org"},
		{ID: "team-b", UserAlias: "Team B", UserEmail: "b@example.com", UserRole: "admin"},
	} {
		if _, err := h.pgUsers.Create(ctx, u); err != nil {
			t.Fatalf("seed Create(%s): %v", u.ID, err)
		}
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/litellm/user/list?page=1&page_size=25", nil)
	h.ListLiteLLMUsersCompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	for _, want := range []string{
		`"users"`,
		`"total":2`,
		`"page":1`,
		`"page_size":25`,
		`"user_id":"team-a"`,
		`"user_alias":"Team A"`,
		`"user_id":"team-b"`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("list response missing %s; body=%s", want, rec.Body.String())
		}
	}
	if bytes.Contains(body, []byte(`"UserAlias"`)) {
		t.Errorf("list response leaked internal casing; body=%s", rec.Body.String())
	}
}

// TestGetLiteLLMUserCompat seeds a user and verifies GET /litellm/user/info
// returns it by user_id, plus a 404 when the user does not exist.
func TestGetLiteLLMUserCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_info")
	gin.SetMode(gin.TestMode)
	if _, err := h.pgUsers.Create(context.Background(), store.InternalUser{
		ID: "team-a", UserAlias: "Team A", UserEmail: "a@example.com", UserRole: "org",
	}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/litellm/user/info?user_id=team-a", nil)
	h.GetLiteLLMUserCompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"user_id":"team-a"`, `"user_alias":"Team A"`, `"user_role":"org"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("info response missing %s; body=%s", want, rec.Body.String())
		}
	}

	// 404 case.
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/v0/management/litellm/user/info?user_id=nope", nil)
	h.GetLiteLLMUserCompat(c2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("missing user status = %d; want 404; body=%s", rec2.Code, rec2.Body.String())
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("missing user response missing not_found type; body=%s", rec2.Body.String())
	}
}

// TestUpdateLiteLLMUserCompat seeds a user, updates user_alias via
// POST /litellm/user/update, and verifies the returned value reflects the change.
func TestUpdateLiteLLMUserCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_update")
	gin.SetMode(gin.TestMode)
	if _, err := h.pgUsers.Create(context.Background(), store.InternalUser{
		ID: "team-a", UserAlias: "Team A", UserEmail: "a@example.com", UserRole: "org",
	}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"user_id":"team-a","user_alias":"Team Alpha","user_role":"admin"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/user/update", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateLiteLLMUserCompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"user_alias":"Team Alpha"`, `"user_role":"admin"`, `"user_id":"team-a"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("update response missing %s; body=%s", want, rec.Body.String())
		}
	}
}

// TestDeleteLiteLLMUserCompat seeds a user, deletes it via POST /litellm/user/delete,
// verifies {"deleted": true}, and confirms a subsequent Get 404s.
func TestDeleteLiteLLMUserCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_delete")
	gin.SetMode(gin.TestMode)
	if _, err := h.pgUsers.Create(context.Background(), store.InternalUser{
		ID: "team-a", UserAlias: "Team A", UserRole: "org",
	}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"user_id":"team-a"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/user/delete", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.DeleteLiteLLMUserCompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"id":"team-a"`, `"deleted":true`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("delete response missing %s; body=%s", want, rec.Body.String())
		}
	}

	// Confirm the user is gone.
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/v0/management/litellm/user/info?user_id=team-a", nil)
	h.GetLiteLLMUserCompat(c2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("post-delete get status = %d; want 404; body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestGenerateLiteLLMKeyCompat seeds a user, POSTs /litellm/key/generate, and
// verifies the plaintext secret is returned once and is actually usable at
// runtime (LookupByHash resolves to the created key). It also covers the 400
// empty-user_id and 404 unknown-owner cases.
func TestGenerateLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_generate")
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	if _, err := h.pgUsers.Create(ctx, store.InternalUser{
		ID: "team-a", UserAlias: "Team A", UserRole: "org",
	}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"user_id":"team-a","models":["gpt-4o"],"alias":"prod-key","max_budget":10}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/key/generate", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.GenerateLiteLLMKeyCompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.Bytes()
	for _, want := range []string{
		`"key"`,
		`"user_id":"team-a"`,
		`"secret"`,
		`"key_alias":"prod-key"`,
		`"models"`,
		`"max_budget"`,
		`"spend"`,
		`"created_at"`,
		`"updated_at"`,
	} {
		if !bytes.Contains(resp, []byte(want)) {
			t.Errorf("generate response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// Extract the plaintext secret and prove it resolves via hash lookup.
	var gen struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(resp, &gen); err != nil {
		t.Fatalf("unmarshal generate response: %v", err)
	}
	if gen.Secret == "" {
		t.Fatalf("generate returned empty secret; body=%s", rec.Body.String())
	}
	key, _, err := h.pgAPIKeys.LookupByHash(ctx, store.HashSecret(gen.Secret))
	if err != nil {
		t.Fatalf("LookupByHash(secret) failed: %v", err)
	}
	if key.ID != gen.Key {
		t.Errorf("LookupByHash resolved key %s; want generated key %s", key.ID, gen.Key)
	}

	// 400: empty user_id.
	rec400 := httptest.NewRecorder()
	c400, _ := gin.CreateTestContext(rec400)
	c400.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/key/generate", bytes.NewBufferString(`{"user_id":""}`))
	c400.Request.Header.Set("Content-Type", "application/json")
	h.GenerateLiteLLMKeyCompat(c400)
	if rec400.Code != http.StatusBadRequest {
		t.Fatalf("empty user_id status = %d; want 400; body=%s", rec400.Code, rec400.Body.String())
	}
	if !bytes.Contains(rec400.Body.Bytes(), []byte(`"invalid_request"`)) {
		t.Errorf("empty user_id response missing invalid_request type; body=%s", rec400.Body.String())
	}

	// 404: unknown owner.
	rec404 := httptest.NewRecorder()
	c404, _ := gin.CreateTestContext(rec404)
	c404.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/key/generate", bytes.NewBufferString(`{"user_id":"nope"}`))
	c404.Request.Header.Set("Content-Type", "application/json")
	h.GenerateLiteLLMKeyCompat(c404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("unknown owner status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
	if !bytes.Contains(rec404.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("unknown owner response missing not_found type; body=%s", rec404.Body.String())
	}

	// 400: tpm_limit is not supported on the runtime policy, so it must be
	// rejected explicitly rather than silently dropped.
	recTpm := httptest.NewRecorder()
	cTpm, _ := gin.CreateTestContext(recTpm)
	cTpm.Request = httptest.NewRequest(http.MethodPost, "/v0/management/litellm/key/generate", bytes.NewBufferString(`{"user_id":"team-a","tpm_limit":100}`))
	cTpm.Request.Header.Set("Content-Type", "application/json")
	h.GenerateLiteLLMKeyCompat(cTpm)
	if recTpm.Code != http.StatusBadRequest {
		t.Fatalf("tpm_limit status = %d; want 400; body=%s", recTpm.Code, recTpm.Body.String())
	}
	if !bytes.Contains(recTpm.Body.Bytes(), []byte(`"invalid_request"`)) {
		t.Errorf("tpm_limit response missing invalid_request type; body=%s", recTpm.Body.String())
	}
}

// TestGetLiteLLMKeyCompat seeds a user + key, GETs /litellm/key/info, and
// verifies the fields are present while the secret is omitted (LiteLLM omits it
// on read). Also covers the 404 case.
func TestGetLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_info")
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	if _, err := h.pgUsers.Create(ctx, store.InternalUser{
		ID: "team-a", UserAlias: "Team A", UserRole: "org",
	}); err != nil {
		t.Fatalf("seed user Create: %v", err)
	}
	pol := store.Policy{AllowedModels: []string{"gpt-4o"}}
	key, _, err := h.pgAPIKeys.Create(ctx, "prod", "prod-key", "", nil, nil, &pol)
	if err != nil {
		t.Fatalf("seed key Create: %v", err)
	}
	if err := h.pgAPIKeys.UpdateUserID(ctx, key.ID, "team-a"); err != nil {
		t.Fatalf("seed UpdateUserID: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/litellm/key/info?key="+key.ID, nil)
	h.GetLiteLLMKeyCompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.Bytes()
	for _, want := range []string{
		`"key":"` + key.ID + `"`,
		`"user_id":"team-a"`,
		`"key_alias":"prod-key"`,
		`"models"`,
		`"spend"`,
		`"created_at"`,
		`"updated_at"`,
	} {
		if !bytes.Contains(resp, []byte(want)) {
			t.Errorf("info response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// The secret must NOT be leaked on read.
	if bytes.Contains(resp, []byte(`"secret"`)) {
		t.Errorf("info response leaked secret; body=%s", rec.Body.String())
	}

	// 404 case.
	rec404 := httptest.NewRecorder()
	c404, _ := gin.CreateTestContext(rec404)
	c404.Request = httptest.NewRequest(http.MethodGet, "/v0/management/litellm/key/info?key=nope", nil)
	h.GetLiteLLMKeyCompat(c404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("missing key status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
	if !bytes.Contains(rec404.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("missing key response missing not_found type; body=%s", rec404.Body.String())
	}
}
