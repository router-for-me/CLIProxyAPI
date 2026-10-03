package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Mirror production Bootstrap: EnsureSchema creates the base tables, but the
	// usage_stat_day rollup table (and the aggregate indexes) are created by
	// Migrate. Run it so the spend endpoints resolve their tables.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, table := range []string{
		pg.InternalUsersTable(),
		pg.APIKeysTable(),
		pg.PoliciesTable(),
		pg.UsageEventsTable(),
		pg.RollupTable(),
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

// seedCompatUser creates an internal user (idempotent on the id).
func seedCompatUser(t *testing.T, h *Handler, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.pgUsers.Get(ctx, id); err == nil {
		return
	} else if !errors.Is(err, store.ErrInternalUserNotFound) {
		t.Fatalf("seed user Get(%s): %v", id, err)
	}
	if _, err := h.pgUsers.Create(ctx, store.InternalUser{
		ID: id, UserAlias: "Team " + strings.ToUpper(id), UserRole: "org",
	}); err != nil {
		t.Fatalf("seed user Create(%s): %v", id, err)
	}
}

// seedCompatKey creates an internal owner user and a runtime API key owned by it,
// returning the key row. The plaintext secret is discarded (only the hash is
// stored); tests that need it use LookupByHash instead.
func seedCompatKey(t *testing.T, h *Handler, name, alias string) *store.APIKey {
	t.Helper()
	ctx := context.Background()
	seedCompatUser(t, h, "team-a")
	pol := store.Policy{AllowedModels: []string{"gpt-4o"}}
	key, _, err := h.pgAPIKeys.Create(ctx, name, alias, "", nil, nil, &pol)
	if err != nil {
		t.Fatalf("seed key Create: %v", err)
	}
	if err := h.pgAPIKeys.UpdateUserID(ctx, key.ID, "team-a"); err != nil {
		t.Fatalf("seed UpdateUserID: %v", err)
	}
	return key
}

// seedCompatSpendEvent inserts a usage event carrying token/cost data so the
// spend compat endpoints have something to aggregate. Returns the event.
func seedCompatSpendEvent(t *testing.T, h *Handler, userID string) store.UsageEvent {
	t.Helper()
	ev := store.UsageEvent{
		RequestID:    "req-usage-compat",
		APIKeyID:     "key-usage-compat",
		UserID:       userID,
		Provider:     "openai",
		Model:        "gpt-4o",
		InputTokens:  100,
		OutputTokens: 50,
		TotalTokens:  150,
		CostUSD:      1.25,
		RequestedAt:  time.Now().Add(-time.Hour).UTC(),
	}
	if err := h.pgUsage.InsertEvent(context.Background(), ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	return ev
}

// doCompat performs one request against the given handler func and returns the
// recorder. Sets the Content-Type header for POST bodies.
func doCompat(h func(c *gin.Context), method, path, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	h(c)
	return rec
}

// ---------------------------------------------------------------------------
// POST /litellm/user/new
// ---------------------------------------------------------------------------

// TestCreateLiteLLMUserCompatRoundTrip verifies POST /litellm/user/new returns
// 200 with LiteLLM's NewUserResponse field names, including the generated
// plaintext key (auto_create_key defaults true) that resolves at runtime.
func TestCreateLiteLLMUserCompatRoundTrip(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_new")
	ctx := context.Background()

	rec := doCompat(h.CreateLiteLLMUserCompat, http.MethodPost,
		"/v0/management/litellm/user/new",
		`{"user_id":"team-a","user_alias":"Team A","user_email":"a@example.com","models":["gpt-4o"],"max_budget":10}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.Bytes()
	for _, want := range []string{
		`"user_id":"team-a"`,
		`"user_alias":"Team A"`,
		`"user_email":"a@example.com"`,
		`"models"`,
		`"max_budget"`,
		`"spend"`,
		`"created_at"`,
		`"updated_at"`,
		`"key"`,
		`"token_id"`,
	} {
		if !bytes.Contains(resp, []byte(want)) {
			t.Errorf("response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// Fields NOT set in the request must be ABSENT (omitempty parity).
	for _, absent := range []string{`"budget_duration"`, `"tpm_limit"`, `"rpm_limit"`, `"metadata"`} {
		if bytes.Contains(resp, []byte(absent)) {
			t.Errorf("response contains unset field %s; want omitted; body=%s", absent, rec.Body.String())
		}
	}
	// The generated key must be a plaintext secret that resolves at runtime.
	var gen struct {
		Key     string `json:"key"`
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal(resp, &gen); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if !strings.HasPrefix(gen.Key, "sk-") {
		t.Errorf("key = %q; want sk- plaintext secret", gen.Key)
	}
	if gen.TokenID == "" {
		t.Error("token_id empty; want the key row id")
	}
	key, _, err := h.pgAPIKeys.LookupByHash(ctx, store.HashSecret(gen.Key))
	if err != nil {
		t.Fatalf("LookupByHash(generated key) failed: %v", err)
	}
	if key.ID != gen.TokenID {
		t.Errorf("LookupByHash resolved %s; want token_id %s", key.ID, gen.TokenID)
	}
	// The internal NixLLM casing must NOT leak into the compat response.
	if bytes.Contains(resp, []byte(`"UserAlias"`)) {
		t.Errorf("response leaked internal casing; body=%s", rec.Body.String())
	}
}

// TestCreateLiteLLMUserCompatNoAutoKey verifies auto_create_key=false suppresses
// the generated key fields (LiteLLM's behavior when the caller manages keys).
func TestCreateLiteLLMUserCompatNoAutoKey(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_new_noauto")

	rec := doCompat(h.CreateLiteLLMUserCompat, http.MethodPost,
		"/v0/management/litellm/user/new",
		`{"user_id":"team-a","auto_create_key":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"key"`)) {
		t.Errorf("auto_create_key=false still returned a key; body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"token_id"`)) {
		t.Errorf("auto_create_key=false still returned token_id; body=%s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /litellm/user/list
// ---------------------------------------------------------------------------

// TestListLiteLLMUsersCompat seeds two users and verifies GET /litellm/user/list
// returns LiteLLM's UserListResponse envelope with total_pages and asc default
// sort_order.
func TestListLiteLLMUsersCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_list")
	ctx := context.Background()
	for _, u := range []store.InternalUser{
		{ID: "team-a", UserAlias: "Team A", UserEmail: "a@example.com", UserRole: "org"},
		{ID: "team-b", UserAlias: "Team B", UserEmail: "b@example.com", UserRole: "admin"},
	} {
		if _, err := h.pgUsers.Create(ctx, u); err != nil {
			t.Fatalf("seed Create(%s): %v", u.ID, err)
		}
	}

	rec := doCompat(h.ListLiteLLMUsersCompat, http.MethodGet,
		"/v0/management/litellm/user/list?page=1&page_size=25", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	for _, want := range []string{
		`"users"`,
		`"total":2`,
		`"page":1`,
		`"page_size":25`,
		`"total_pages":1`,
		`"user_id":"team-a"`,
		`"user_alias":"Team A"`,
		`"user_id":"team-b"`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("list response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// Default sort_order is asc: team-a (alias "Team A") must precede team-b
	// ("Team B") — an asc listing of the aliases.
	if aIdx, bIdx := bytes.Index(body, []byte(`"user_alias":"Team A"`)), bytes.Index(body, []byte(`"user_alias":"Team B"`)); aIdx < 0 || bIdx < 0 || aIdx > bIdx {
		t.Errorf("default sort_order not asc by alias; aIdx=%d bIdx=%d body=%s", aIdx, bIdx, rec.Body.String())
	}
	if bytes.Contains(body, []byte(`"UserAlias"`)) {
		t.Errorf("list response leaked internal casing; body=%s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /litellm/user/info
// ---------------------------------------------------------------------------

// TestGetLiteLLMUserCompat seeds a user + key and verifies GET /litellm/user/info
// returns LiteLLM's UserInfoResponse shape {user_id, user_info, keys, teams}.
func TestGetLiteLLMUserCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_info")
	seedCompatKey(t, h, "prod", "prod-key")

	rec := doCompat(h.GetLiteLLMUserCompat, http.MethodGet,
		"/v0/management/litellm/user/info?user_id=team-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	for _, want := range []string{
		`"user_id":"team-a"`,
		`"user_info"`,
		`"keys"`,
		`"teams"`,
		`"user_alias"`,
		`"user_role"`,
		`"key_alias":"prod-key"`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("info response missing %s; body=%s", want, rec.Body.String())
		}
	}

	// 404 case.
	rec404 := doCompat(h.GetLiteLLMUserCompat, http.MethodGet,
		"/v0/management/litellm/user/info?user_id=nope", "")
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("missing user status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
	if !bytes.Contains(rec404.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("missing user response missing not_found type; body=%s", rec404.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /litellm/user/update
// ---------------------------------------------------------------------------

// TestUpdateLiteLLMUserCompat seeds a user, updates user_alias via
// POST /litellm/user/update, and verifies the returned value reflects the change.
func TestUpdateLiteLLMUserCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_update")
	seedCompatUser(t, h, "team-a")

	rec := doCompat(h.UpdateLiteLLMUserCompat, http.MethodPost,
		"/v0/management/litellm/user/update",
		`{"user_id":"team-a","user_alias":"Team Alpha","user_role":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"user_alias":"Team Alpha"`, `"user_role":"admin"`, `"user_id":"team-a"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("update response missing %s; body=%s", want, rec.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// POST /litellm/user/delete
// ---------------------------------------------------------------------------

// TestDeleteLiteLLMUserCompat seeds a user, deletes it via POST /litellm/user/delete
// with the spec body {"user_ids": [...]}, and verifies the deleted_users count
// plus that the user (and its keys) are gone.
func TestDeleteLiteLLMUserCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_delete")
	ctx := context.Background()
	seedCompatKey(t, h, "prod", "prod-key")
	seedCompatUser(t, h, "team-b")

	rec := doCompat(h.DeleteLiteLLMUserCompat, http.MethodPost,
		"/v0/management/litellm/user/delete",
		`{"user_ids":["team-a","team-b"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	// LiteLLM's delete_user returns the raw delete count (an int), not an
	// envelope.
	var deleted int
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if deleted != 2 {
		t.Errorf("deleted_users = %d; want 2", deleted)
	}

	// Confirm both users are gone.
	if _, err := h.pgUsers.Get(ctx, "team-a"); !errors.Is(err, store.ErrInternalUserNotFound) {
		t.Errorf("post-delete Get(team-a) err = %v; want ErrInternalUserNotFound", err)
	}
	if _, err := h.pgUsers.Get(ctx, "team-b"); !errors.Is(err, store.ErrInternalUserNotFound) {
		t.Errorf("post-delete Get(team-b) err = %v; want ErrInternalUserNotFound", err)
	}
	// Confirm team-a's key was deleted too.
	keys, _, err := h.pgAPIKeys.ListPagedFiltered(ctx, 1, 100, store.APIKeyListFilter{UserID: "team-a"})
	if err != nil {
		t.Fatalf("ListPagedFiltered: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("user delete left %d keys behind; want 0", len(keys))
	}
}

// TestDeleteLiteLLMUserCompatMissingUser verifies a batch delete skips an
// unknown user id without erroring (LiteLLM's count semantics).
func TestDeleteLiteLLMUserCompatMissingUser(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_delete_missing")

	rec := doCompat(h.DeleteLiteLLMUserCompat, http.MethodPost,
		"/v0/management/litellm/user/delete",
		`{"user_ids":["nope"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "0" {
		t.Errorf("deleted_users = %s; want 0", got)
	}
}

// ---------------------------------------------------------------------------
// POST /litellm/key/generate
// ---------------------------------------------------------------------------

// TestGenerateLiteLLMKeyCompat seeds a user, POSTs /litellm/key/generate, and
// verifies the plaintext secret is returned once (GenerateKeyResponse) and is
// actually usable at runtime (LookupByHash resolves to the created key). It
// also covers the 400 empty-user_id and 404 unknown-owner cases.
func TestGenerateLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_generate")
	ctx := context.Background()
	seedCompatUser(t, h, "team-a")

	rec := doCompat(h.GenerateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/generate",
		`{"user_id":"team-a","models":["gpt-4o"],"alias":"prod-key","max_budget":10,"tpm_limit":400,"budget_duration":"30d"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.Bytes()
	for _, want := range []string{
		`"key"`,
		`"user_id":"team-a"`,
		`"key_alias":"prod-key"`,
		`"models"`,
		`"max_budget"`,
		`"spend"`,
		`"created_at"`,
		`"updated_at"`,
		`"token_id"`,
	} {
		if !bytes.Contains(resp, []byte(want)) {
			t.Errorf("generate response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// Extract the plaintext secret and prove it resolves via hash lookup. The
	// response key is the plaintext (not the internal id).
	var gen struct {
		Key     string `json:"key"`
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal(resp, &gen); err != nil {
		t.Fatalf("unmarshal generate response: %v", err)
	}
	if !strings.HasPrefix(gen.Key, "sk-") {
		t.Fatalf("key = %q; want sk- plaintext; body=%s", gen.Key, rec.Body.String())
	}
	key, _, err := h.pgAPIKeys.LookupByHash(ctx, store.HashSecret(gen.Key))
	if err != nil {
		t.Fatalf("LookupByHash(secret) failed: %v", err)
	}
	if key.ID != gen.TokenID {
		t.Errorf("LookupByHash resolved %s; want token_id %s", key.ID, gen.TokenID)
	}

	// 400: empty user_id.
	rec400 := doCompat(h.GenerateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/generate", `{"user_id":""}`)
	if rec400.Code != http.StatusBadRequest {
		t.Fatalf("empty user_id status = %d; want 400; body=%s", rec400.Code, rec400.Body.String())
	}
	if !bytes.Contains(rec400.Body.Bytes(), []byte(`"invalid_request"`)) {
		t.Errorf("empty user_id response missing invalid_request type; body=%s", rec400.Body.String())
	}

	// 404: unknown owner.
	rec404 := doCompat(h.GenerateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/generate", `{"user_id":"nope"}`)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("unknown owner status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
	if !bytes.Contains(rec404.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("unknown owner response missing not_found type; body=%s", rec404.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /litellm/key/info
// ---------------------------------------------------------------------------

// TestGetLiteLLMKeyCompat seeds a user + key, GETs /litellm/key/info by the
// internal id, and verifies the LiteLLM {"key": <echoed>, "info": {…}} envelope
// with the secret omitted on read. Also covers hash lookup and the 404 case.
func TestGetLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_info")
	key := seedCompatKey(t, h, "prod", "prod-key")

	rec := doCompat(h.GetLiteLLMKeyCompat, http.MethodGet,
		"/v0/management/litellm/key/info?key="+key.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.Bytes()
	for _, want := range []string{
		`"key":"` + key.ID + `"`,
		`"info"`,
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
	rec404 := doCompat(h.GetLiteLLMKeyCompat, http.MethodGet,
		"/v0/management/litellm/key/info?key=nope", "")
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("missing key status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
	if !bytes.Contains(rec404.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("missing key response missing not_found type; body=%s", rec404.Body.String())
	}
}

// TestGetLiteLLMKeyCompatByHash verifies /litellm/key/info resolves a sha256
// hash (the LiteLLM wire contract for hash-lookup) to the same key.
func TestGetLiteLLMKeyCompatByHash(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_info_hash")
	ctx := context.Background()
	key, _, err := h.pgAPIKeys.Create(ctx, "prod", "prod-key", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	if err := h.pgAPIKeys.UpdateUserID(ctx, key.ID, "team-a"); err != nil {
		t.Fatalf("seed UpdateUserID: %v", err)
	}

	rec := doCompat(h.GetLiteLLMKeyCompat, http.MethodGet,
		"/v0/management/litellm/key/info?key="+key.KeyHash, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"key_alias":"prod-key"`)) {
		t.Errorf("hash lookup response missing key_alias; body=%s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /litellm/key/list
// ---------------------------------------------------------------------------

// TestListLiteLLMKeysCompat seeds a user + two keys and verifies GET
// /litellm/key/list returns LiteLLM's KeyListResponseObject fields
// (keys/total_count/current_page/total_pages) with the size param.
func TestListLiteLLMKeysCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_list")
	k1 := seedCompatKey(t, h, "prod", "prod-key")
	k2 := seedCompatKey(t, h, "dev", "dev-key")

	rec := doCompat(h.ListLiteLLMKeysCompat, http.MethodGet,
		"/v0/management/litellm/key/list?page=1&size=25", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	for _, want := range []string{
		`"keys"`,
		`"total_count":2`,
		`"current_page":1`,
		`"total_pages":1`,
		`"key":"` + k1.ID + `"`,
		`"key_alias":"prod-key"`,
		`"key":"` + k2.ID + `"`,
		`"key_alias":"dev-key"`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("list response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// The secret must NOT be leaked on read.
	if bytes.Contains(body, []byte(`"secret"`)) {
		t.Errorf("list response leaked secret; body=%s", rec.Body.String())
	}
	// size is clamped to 100 (LiteLLM's max).
	recClamped := doCompat(h.ListLiteLLMKeysCompat, http.MethodGet,
		"/v0/management/litellm/key/list?size=9999", "")
	if recClamped.Code != http.StatusOK {
		t.Fatalf("clamped status = %d; want 200; body=%s", recClamped.Code, recClamped.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /litellm/key/update
// ---------------------------------------------------------------------------

// TestUpdateLiteLLMKeyCompat seeds a user + key, POSTs /litellm/key/update
// changing name/status/alias, and verifies the returned values reflect the
// changes.
func TestUpdateLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_update")
	ctx := context.Background()
	key := seedCompatKey(t, h, "prod", "prod-key")

	rec := doCompat(h.UpdateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/update",
		`{"key":"`+key.ID+`","name":"prod-v2","status":"disabled","alias":"prod-alias"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"key":"` + key.ID + `"`, `"key_alias":"prod-alias"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("update response missing %s; body=%s", want, rec.Body.String())
		}
	}
	// Confirm the name/status persisted on the row.
	reloaded, _, err := h.pgAPIKeys.LookupByID(ctx, key.ID)
	if err != nil {
		t.Fatalf("reload LookupByID: %v", err)
	}
	if reloaded.Name != "prod-v2" {
		t.Errorf("reloaded name = %q; want prod-v2", reloaded.Name)
	}
	if reloaded.Status != "disabled" {
		t.Errorf("reloaded status = %q; want disabled", reloaded.Status)
	}

	// Updating a nonexistent key must return 404 (not 500).
	rec404 := doCompat(h.UpdateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/update", `{"key":"nope","name":"x"}`)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("missing key update status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
	if !bytes.Contains(rec404.Body.Bytes(), []byte(`"not_found"`)) {
		t.Errorf("missing key update response missing not_found type; body=%s", rec404.Body.String())
	}
}

// TestUpdateLiteLLMKeyCompatByAlias verifies /litellm/key/update accepts the
// key_alias identifier (LiteLLM's UpdateKeyRequest alternative).
func TestUpdateLiteLLMKeyCompatByAlias(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_update_alias")
	key := seedCompatKey(t, h, "prod", "prod-key")

	rec := doCompat(h.UpdateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/update",
		`{"key_alias":"prod-key","name":"prod-renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	reloaded, _, err := h.pgAPIKeys.LookupByID(context.Background(), key.ID)
	if err != nil {
		t.Fatalf("reload LookupByID: %v", err)
	}
	if reloaded.Name != "prod-renamed" {
		t.Errorf("reloaded name = %q; want prod-renamed", reloaded.Name)
	}
}

// ---------------------------------------------------------------------------
// POST /litellm/key/regenerate
// ---------------------------------------------------------------------------

// TestRegenerateLiteLLMKeyCompat seeds a user + key, POSTs /litellm/key/regenerate
// with the key as a QUERY param (the spec contract), and verifies the new
// plaintext secret is returned AND that rotation actually happened: the old
// secret no longer resolves while the new one does.
func TestRegenerateLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_regenerate")
	ctx := context.Background()
	key := seedCompatKey(t, h, "prod", "prod-key")

	// Recover the current plaintext secret so we can prove rotation.
	seedKey, _, err := h.pgAPIKeys.LookupByID(ctx, key.ID)
	if err != nil {
		t.Fatalf("seed LookupByID: %v", err)
	}
	if seedKey.KeyHash == "" {
		t.Fatal("seeded key has no secret hash")
	}

	rec := doCompat(h.RegenerateLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/regenerate?key="+key.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key     string `json:"key"`
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal regenerate response: %v", err)
	}
	if !strings.HasPrefix(resp.Key, "sk-") {
		t.Fatalf("key = %q; want sk- plaintext; body=%s", resp.Key, rec.Body.String())
	}
	if resp.TokenID != key.ID {
		t.Errorf("token_id = %q; want %q", resp.TokenID, key.ID)
	}
	newHash := store.HashSecret(resp.Key)
	if newHash == seedKey.KeyHash {
		t.Fatal("regenerate returned the same secret; expected rotation")
	}
	// The old secret hash must no longer resolve.
	if _, _, err := h.pgAPIKeys.LookupByHash(ctx, seedKey.KeyHash); !errors.Is(err, store.ErrAPIKeyNotFound) {
		t.Errorf("old secret still resolves after regenerate; err=%v", err)
	}
	// The new secret must resolve to the same key id.
	newKey, _, err := h.pgAPIKeys.LookupByHash(ctx, newHash)
	if err != nil {
		t.Fatalf("LookupByHash(new secret) failed: %v", err)
	}
	if newKey.ID != key.ID {
		t.Errorf("LookupByHash(new) resolved key %s; want %s", newKey.ID, key.ID)
	}
}

// ---------------------------------------------------------------------------
// POST /litellm/key/delete
// ---------------------------------------------------------------------------

// TestDeleteLiteLLMKeyCompat seeds a user + key, POSTs /litellm/key/delete with
// the spec body {"keys": [...]}, verifies {"deleted_keys": [...]}, and confirms
// the key is gone.
func TestDeleteLiteLLMKeyCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_delete")
	ctx := context.Background()
	k1 := seedCompatKey(t, h, "prod", "prod-key")
	k2 := seedCompatKey(t, h, "dev", "dev-key")

	rec := doCompat(h.DeleteLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/delete",
		`{"keys":["`+k1.ID+`","`+k2.ID+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var del struct {
		DeletedKeys []string `json:"deleted_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &del); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(del.DeletedKeys) != 2 {
		t.Errorf("deleted_keys len = %d; want 2; body=%s", len(del.DeletedKeys), rec.Body.String())
	}
	// Confirm both keys are gone.
	if _, _, err := h.pgAPIKeys.LookupByID(ctx, k1.ID); !errors.Is(err, store.ErrAPIKeyNotFound) {
		t.Errorf("post-delete LookupByID(k1) err = %v; want ErrAPIKeyNotFound", err)
	}
	if _, _, err := h.pgAPIKeys.LookupByID(ctx, k2.ID); !errors.Is(err, store.ErrAPIKeyNotFound) {
		t.Errorf("post-delete LookupByID(k2) err = %v; want ErrAPIKeyNotFound", err)
	}
}

// TestDeleteLiteLLMKeyCompatByAliases verifies /litellm/key/delete accepts the
// spec's {"key_aliases": [...]} alternative body.
func TestDeleteLiteLLMKeyCompatByAliases(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_delete_aliases")
	seedCompatKey(t, h, "prod", "prod-key")

	rec := doCompat(h.DeleteLiteLLMKeyCompat, http.MethodPost,
		"/v0/management/litellm/key/delete",
		`{"key_aliases":["prod-key"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var del struct {
		DeletedKeys []string `json:"deleted_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &del); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(del.DeletedKeys) != 1 || del.DeletedKeys[0] != "prod-key" {
		t.Errorf("deleted_keys = %v; want [prod-key]", del.DeletedKeys)
	}
}

// ---------------------------------------------------------------------------
// GET /litellm/spend/logs
// ---------------------------------------------------------------------------

// TestListLiteLLMSpendLogsCompat verifies GET /litellm/spend/logs returns a
// DIRECT array of LiteLLM spend-log rows (not a paginated envelope) and honors
// the user_id filter.
func TestListLiteLLMSpendLogsCompat(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_spend_logs")
	seedCompatSpendEvent(t, h, "team-a")

	rec := doCompat(h.ListLiteLLMSpendLogsCompat, http.MethodGet,
		"/v0/management/litellm/spend/logs?user_id=team-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The response is a DIRECT array: the first non-whitespace byte is '['.
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		t.Fatalf("spend/logs response is not a direct array; body=%s", rec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d; want 1; body=%s", len(rows), rec.Body.String())
	}
	row := rows[0]
	if got := row["model"]; got != "gpt-4o" {
		t.Errorf("model = %v; want gpt-4o", got)
	}
	if got := row["spend"]; got != float64(1.25) {
		t.Errorf("spend = %v; want 1.25", got)
	}
	for _, field := range []string{"request_id", "total_tokens", "prompt_tokens", "completion_tokens", "startTime", "endTime", "call_type", "status"} {
		if _, ok := row[field]; !ok {
			t.Errorf("spend/logs row missing %q; row=%v", field, row)
		}
	}

	// A different user yields an empty array.
	rec2 := doCompat(h.ListLiteLLMSpendLogsCompat, http.MethodGet,
		"/v0/management/litellm/spend/logs?user_id=other", "")
	if got := strings.TrimSpace(rec2.Body.String()); got != "[]" {
		t.Errorf("other-user rows = %s; want []", got)
	}
}

// TestListLiteLLMSpendLogsCompatAPIKeyFilter verifies the api_key filter accepts
// the same non-secret label the endpoint projects (key_alias, falling back to
// the key name) as well as the raw internal key id. Regression: filtering by
// the returned api_key value previously matched api_key_id only and returned an
// empty array.
func TestListLiteLLMSpendLogsCompatAPIKeyFilter(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_spend_logs_keyfilter")
	ctx := context.Background()
	// One key with an alias and one with only a name, so both label fallbacks
	// are exercised alongside the raw internal id.
	aliased := seedCompatKey(t, h, "prod", "prod-key")
	named := seedCompatKey(t, h, "playground", "")
	for _, key := range []*store.APIKey{aliased, named} {
		ev := store.UsageEvent{
			RequestID:   "req-keyfilter-" + key.Name,
			APIKeyID:    key.ID,
			UserID:      "team-a",
			Provider:    "litellm",
			Model:       "gpt-4o",
			TotalTokens: 150,
			CostUSD:     1.25,
			RequestedAt: time.Now().Add(-time.Hour).UTC(),
		}
		if err := h.pgUsage.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	// One event per key, so filtering by any single key's label yields exactly one.
	for _, key := range []*store.APIKey{aliased, named} {
		label := key.KeyAlias
		if label == "" {
			label = key.Name
		}
		for _, param := range []string{label, key.ID} {
			rec := doCompat(h.ListLiteLLMSpendLogsCompat, http.MethodGet,
				"/v0/management/litellm/spend/logs?api_key="+param, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("api_key=%s status = %d; want 200; body=%s", param, rec.Code, rec.Body.String())
			}
			var rows []map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatalf("api_key=%s unmarshal: %v; body=%s", param, err, rec.Body.String())
			}
			if len(rows) != 1 {
				t.Errorf("api_key=%s rows = %d; want 1; body=%s", param, len(rows), rec.Body.String())
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Spend reports
// ---------------------------------------------------------------------------

// TestGetLiteLLMGlobalSpendReport verifies GET /litellm/global/spend/report
// returns the LiteLLM per-api_key spend report shape (api_key/total_cost/
// total_input_tokens/total_output_tokens/model_details) and requires both
// start_date and end_date.
func TestGetLiteLLMGlobalSpendReport(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_global_spend_report")
	ev := seedCompatSpendEvent(t, h, "team-a")

	start := ev.RequestedAt.Add(-time.Hour).Format("2006-01-02")
	end := ev.RequestedAt.Add(time.Hour).Format("2006-01-02")
	rec := doCompat(h.GetLiteLLMGlobalSpendReport, http.MethodGet,
		"/v0/management/litellm/global/spend/report?start_date="+start+"&end_date="+end, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d; want 1; body=%s", len(rows), rec.Body.String())
	}
	row := rows[0]
	if got := row["api_key"]; got != "key-usage-compat" {
		t.Errorf("api_key = %v; want key-usage-compat", got)
	}
	if got := row["total_cost"]; got != float64(1.25) {
		t.Errorf("total_cost = %v; want 1.25", got)
	}
	if got := row["total_input_tokens"]; got != float64(100) {
		t.Errorf("total_input_tokens = %v; want 100", got)
	}
	if got := row["total_output_tokens"]; got != float64(50) {
		t.Errorf("total_output_tokens = %v; want 50", got)
	}
	details, ok := row["model_details"].([]any)
	if !ok || len(details) != 1 {
		t.Fatalf("model_details = %v; want 1 entry", row["model_details"])
	}
	md := details[0].(map[string]any)
	if got := md["model"]; got != "gpt-4o" {
		t.Errorf("model_details[0].model = %v; want gpt-4o", got)
	}
	if got := md["total_cost"]; got != float64(1.25) {
		t.Errorf("model_details[0].total_cost = %v; want 1.25", got)
	}

	// Missing date params must 400 (LiteLLM's contract).
	rec400 := doCompat(h.GetLiteLLMGlobalSpendReport, http.MethodGet,
		"/v0/management/litellm/global/spend/report", "")
	if rec400.Code != http.StatusBadRequest {
		t.Fatalf("missing dates status = %d; want 400; body=%s", rec400.Code, rec400.Body.String())
	}
}

// TestGetLiteLLMKeySpendReport verifies GET /litellm/key/spend/report scopes by
// api_key and returns the same spend-report shape.
func TestGetLiteLLMKeySpendReport(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_key_spend_report")
	ctx := context.Background()
	// Seed a real runtime key and attach the spend event to it so the api_key
	// query param (an internal key id here) resolves to the row.
	key := seedCompatKey(t, h, "prod", "prod-key")
	ev := store.UsageEvent{
		RequestID:   "req-usage-compat",
		APIKeyID:    key.ID,
		UserID:      "team-a",
		Provider:    "openai",
		Model:       "gpt-4o",
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
		CostUSD:     1.25,
		RequestedAt: time.Now().Add(-time.Hour).UTC(),
	}
	if err := h.pgUsage.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	start := ev.RequestedAt.Add(-time.Hour).Format("2006-01-02")
	end := ev.RequestedAt.Add(time.Hour).Format("2006-01-02")
	rec := doCompat(h.GetLiteLLMKeySpendReport, http.MethodGet,
		"/v0/management/litellm/key/spend/report?start_date="+start+"&end_date="+end+"&api_key="+key.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(rows) != 1 || rows[0]["api_key"] != key.ID {
		t.Fatalf("rows = %v; want 1 row for %s", rows, key.ID)
	}
	if got := rows[0]["total_cost"]; got != float64(1.25) {
		t.Errorf("total_cost = %v; want 1.25", got)
	}

	// A different (unknown) key must 404.
	rec404 := doCompat(h.GetLiteLLMKeySpendReport, http.MethodGet,
		"/v0/management/litellm/key/spend/report?start_date="+start+"&end_date="+end+"&api_key=sk-unknown", "")
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("unknown key status = %d; want 404; body=%s", rec404.Code, rec404.Body.String())
	}
}

// TestGetLiteLLMUserSpendReport verifies GET /litellm/user/spend/report scopes by
// internal_user_id and returns the spend-report shape.
func TestGetLiteLLMUserSpendReport(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_user_spend_report")
	ev := seedCompatSpendEvent(t, h, "team-a")

	start := ev.RequestedAt.Add(-time.Hour).Format("2006-01-02")
	end := ev.RequestedAt.Add(time.Hour).Format("2006-01-02")
	rec := doCompat(h.GetLiteLLMUserSpendReport, http.MethodGet,
		"/v0/management/litellm/user/spend/report?start_date="+start+"&end_date="+end+"&internal_user_id=team-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(rows) != 1 || rows[0]["api_key"] != "key-usage-compat" {
		t.Fatalf("rows = %v; want 1 row for key-usage-compat", rows)
	}
}

// TestGetLiteLLMSpendTags verifies GET /litellm/spend/tags returns an empty
// array (the runtime has no request_tags column to aggregate).
func TestGetLiteLLMSpendTags(t *testing.T) {
	h := newTestLiteLLMCompatHandler(t, "mgmt_litellm_spend_tags")

	rec := doCompat(h.GetLiteLLMSpendTags, http.MethodGet, "/v0/management/litellm/spend/tags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("spend/tags body = %s; want []", got)
	}
}
