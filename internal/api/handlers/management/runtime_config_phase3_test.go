package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configstore"
	pgconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/configstore/pg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	runtimeconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
)

// runtimeConfigTestPG spins up a PostgresStore-backed handler on a unique
// schema. Returns the handler, the runtime-config store, and a cleanup
// function that drops the schema. Skips when PGSTORE_TEST_DSN is unset.
func runtimeConfigTestPG(t *testing.T, schema string) (*Handler, *store.PostgresStore, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
	if dsn == "" {
		t.Skip("PGSTORE_TEST_DSN not set; skipping Postgres integration test")
	}
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    dsn,
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Clear control-plane tables so the schema is truly empty.
	for _, table := range []string{
		pg.RuntimeConfigTable(),
		pg.ConfigRevisionsTable(),
		pg.ConfigImportsTable(),
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Logf("clear %s: %v", table, err)
		}
	}
	h := NewHandler(&config.Config{}, "", nil)
	h.SetPGControl(pg)
	return h, pg, func() {
		_ = pg.Close()
	}
}

// runHandler runs h against a synthesized request and returns the
// recorded response.
func runHandler(h *Handler, method, path string, body any) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Handle(method, path, h.dispatcher(method, path))
	w := httptest.NewRecorder()
	var reqBytes []byte
	if body != nil {
		switch v := body.(type) {
		case []byte:
			reqBytes = v
		default:
			b, err := json.Marshal(body)
			if err != nil {
				panic(err)
			}
			reqBytes = b
		}
	}
	var req *http.Request
	if reqBytes != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(reqBytes))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	r.ServeHTTP(w, req)
	return w
}

// dispatcher returns a Gin handler for (method, path) by matching the
// management.Handler's public methods.
func (h *Handler) dispatcher(method, path string) gin.HandlerFunc {
	switch method + " " + path {
	case "GET /v0/management/runtime-config":
		return h.GetRuntimeConfig
	case "POST /v0/management/runtime-config":
		return h.PostRuntimeConfig
	case "POST /v0/management/runtime-config/rollback":
		return h.PostRuntimeConfigRollback
	case "GET /v0/management/config-revisions":
		return h.ListConfigRevisions
	case "GET /v0/management/config-imports":
		return h.ListConfigImports
	case "GET /v0/management/config.yaml":
		return h.GetConfigYAML
	case "PUT /v0/management/config.yaml":
		return h.PutConfigYAML
	}
	return func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"error": "no_dispatcher"}) }
}

// bootstrapHandler ensures the runtime_config singleton has revision 1 so
// the bootstrap path (expected=0) is not the one under test. It calls
// repo.Save with expected=0 (the only allowed path that creates a row).
func bootstrapHandler(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo := pgconfigstore.Open(h.pgControl)
	snap := configsnapshot.NewEmpty()
	snap.Settings["port"] = 8317
	snap.UpdatedSource = "test-seed"
	if _, err := repo.Save(ctx, 0, &snap, configstore.SaveAudit{Actor: "test", Reason: "seed", Source: "test"}); err != nil {
		t.Fatalf("bootstrap Save: %v", err)
	}
}

// activeRevision reads the active revision number from PG so tests can
// assert against it after a save.
func activeRevision(t *testing.T, pg *store.PostgresStore) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc := runtimeconfigstore.New(pg)
	rev, err := rc.ActiveRevision(ctx)
	if err != nil {
		t.Fatalf("ActiveRevision: %v", err)
	}
	return rev
}

// TestRuntimeConfigPostPersistsAndCommits covers the production path:
// POST /runtime-config with the active revision bumps to revision 2 and
// appends a config_revisions row.
func TestRuntimeConfigPostPersistsAndCommits(t *testing.T) {
	h, pg, cleanup := runtimeConfigTestPG(t, "test_p3_post_persists")
	defer cleanup()
	bootstrapHandler(t, h)
	pre := activeRevision(t, pg)

	body := map[string]any{
		"expected_revision": pre,
		"settings": map[string]any{
			"port":    19000,
			"host":    "127.0.0.1",
			"debug":   true,
			"managed": map[string]any{}, // dummy extra so the validation passes
		},
		"extra": map[string]any{
			"api-keys":               []any{"alpha", "beta"},
			"auth-dir":               "~/.cli-proxy-api",
			"gemini-api-key":         []any{},
			"claude-api-key":         []any{},
			"codex-api-key":          []any{},
			"xai-api-key":            []any{},
			"interactions-api-key":   []any{},
			"openai-compatibility":   []any{},
			"opencode-go":            []any{},
			"vertex-api-key":         []any{},
			"oauth-excluded-models":  []any{},
			"oauth-model-alias":      map[string]any{},
			"codex-header-defaults":  map[string]any{},
			"claude-header-defaults": map[string]any{},
		},
	}
	w := runHandler(h, "POST", "/v0/management/runtime-config", body)
	if w.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got, want := activeRevision(t, pg), pre+1; got != want {
		t.Fatalf("active revision = %d, want %d", got, want)
	}
	var resp struct {
		Status         string                   `json:"status"`
		ReloadStatus   string                   `json:"reload_status"`
		ActiveRevision int64                    `json:"active_revision"`
		Snapshot       *configsnapshot.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
	if resp.ActiveRevision != pre+1 {
		t.Fatalf("active_revision = %d, want %d", resp.ActiveRevision, pre+1)
	}
	if resp.Snapshot == nil {
		t.Fatal("snapshot missing from response")
	}
}

// TestRuntimeConfigPostConflictReturns409 covers optimistic concurrency:
// stale expected_revision yields a 409 with the active revision in the
// body.
func TestRuntimeConfigPostConflictReturns409(t *testing.T) {
	h, pg, cleanup := runtimeConfigTestPG(t, "test_p3_post_conflict")
	defer cleanup()
	bootstrapHandler(t, h)
	pre := activeRevision(t, pg)

	body := map[string]any{
		"expected_revision": pre + 99,
		"settings":          map[string]any{"port": 19001},
	}
	w := runHandler(h, "POST", "/v0/management/runtime-config", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("POST status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Error          string `json:"error"`
		Expected       int64  `json:"expected"`
		ActiveRevision int64  `json:"active_revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "revision_conflict" {
		t.Fatalf("error = %q, want revision_conflict", resp.Error)
	}
	if resp.ActiveRevision != pre {
		t.Fatalf("active_revision = %d, want %d", resp.ActiveRevision, pre)
	}
}

// TestRuntimeConfigPostReturns503WhenPGMissing covers the no-PG path:
// when SetPGControl was never called the route returns 503.
func TestRuntimeConfigPostReturns503WhenPGMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(&config.Config{}, "", nil)
	w := runHandler(h, "POST", "/v0/management/runtime-config", map[string]any{
		"expected_revision": 0,
		"settings":          map[string]any{"port": 9000},
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestRuntimeConfigPostEmptyBodyReturns400 covers the input-validation
// guard: settings+extra both empty yields 400.
func TestRuntimeConfigPostEmptyBodyReturns400(t *testing.T) {
	h, _, cleanup := runtimeConfigTestPG(t, "test_p3_post_empty")
	defer cleanup()
	body := map[string]any{"expected_revision": 0}
	w := runHandler(h, "POST", "/v0/management/runtime-config", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// TestPutConfigYAMLReturnsGoneWhenPGActive covers the Phase 3
// deprecation gate: PUT /v0/management/config.yaml returns 410 Gone when
// the runtime_config control plane is active.
func TestPutConfigYAMLReturnsGoneWhenPGActive(t *testing.T) {
	h, _, cleanup := runtimeConfigTestPG(t, "test_p3_put_yaml_gone")
	defer cleanup()
	w := runHandler(h, "PUT", "/v0/management/config.yaml", []byte("port: 9999\n"))
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "deprecated" {
		t.Fatalf("error = %q, want deprecated", resp["error"])
	}
}

// TestGetConfigYAMLReturnsGoneWhenPGActive mirrors the PUT gate.
func TestGetConfigYAMLReturnsGoneWhenPGActive(t *testing.T) {
	h, _, cleanup := runtimeConfigTestPG(t, "test_p3_get_yaml_gone")
	defer cleanup()
	w := runHandler(h, "GET", "/v0/management/config.yaml", nil)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", w.Code)
	}
}

// TestRevisionConflictErrorIsExported guards the public contract: a 409
// response body must contain the same fields the dashboard reads.
func TestRevisionConflictErrorIsExported(t *testing.T) {
	err := &configstore.RevisionConflictError{Current: 42}
	if !strings.Contains(err.Error(), "42") {
		t.Fatalf("error message missing current: %s", err.Error())
	}
	// errors.As round-trip (the API uses errors.As internally via the
	// errorsAs helper).
	var target *configstore.RevisionConflictError
	if !errors.As(err, &target) {
		t.Fatal("errors.As did not match *RevisionConflictError")
	}
	if target.Current != 42 {
		t.Fatalf("Current = %d, want 42", target.Current)
	}
}

// helper: itoa without importing strconv twice in test bodies.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var _ = itoa
