package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// errorDiagnosticsRouter builds a minimal router with the three error-diagnostic
// handlers, plus the existing /errors route (so :id never shadows the static
// paths — we test this explicitly). Mirrors newPGRouter from api_keys_pg_test.go.
func errorDiagnosticsRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/usage-stats/errors", h.GetUsageErrors)
	g.GET("/usage-stats/errors/summary", h.GetErrorSummary)
	g.GET("/usage-stats/errors/groups", h.GetErrorGroups)
	g.GET("/usage-stats/errors/timeline", h.GetErrorTimeline)
	// The :id wildcard route must NOT shadow the static paths above.
	g.GET("/usage-stats/errors/:id", h.GetUsageError)
	return r
}

// TestErrorDiagnosticsReturn503WhenNotConfigured verifies the three new routes
// follow the same PG-backed contract as the existing errors routes: without a
// PG store wired, requirePG returns 503.
func TestErrorDiagnosticsReturn503WhenNotConfigured(t *testing.T) {
	h := newBareHandler()
	r := errorDiagnosticsRouter(h)

	cases := []struct {
		name string
		path string
	}{
		{"summary", "/v0/management/usage-stats/errors/summary"},
		{"groups", "/v0/management/usage-stats/errors/groups"},
		{"timeline", "/v0/management/usage-stats/errors/timeline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("%s: status = %d; want 503; body=%s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestErrorDiagnosticsInvalidGroupBy verifies that GetErrorGroups returns 400
// for unsupported group_by values without hitting the store (handler-side
// validation). This test does NOT require PG.
func TestErrorDiagnosticsInvalidGroupBy(t *testing.T) {
	h := newBareHandler()
	r := errorDiagnosticsRouter(h)

	cases := []struct {
		name    string
		groupBy string
	}{
		{"unknown value", "bogus"},
		{"empty string", ""},
		{"trailing whitespace", "class "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			path := "/v0/management/usage-stats/errors/groups"
			if tc.groupBy != "" {
				q := url.Values{}
				q.Set("group_by", tc.groupBy)
				path += "?" + q.Encode()
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			r.ServeHTTP(w, req)

			// Without PG, requirePG returns 503 before we reach validation.
			if w.Code == http.StatusServiceUnavailable {
				return
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// TestErrorDiagnosticsInvalidInterval verifies that GetErrorTimeline returns
// 400 for unsupported interval values via handler-side validation.
func TestErrorDiagnosticsInvalidInterval(t *testing.T) {
	h := newBareHandler()
	r := errorDiagnosticsRouter(h)

	cases := []struct {
		name     string
		interval string
	}{
		{"unknown value", "year"},
		{"empty string", ""},
		{"trailing whitespace", "hour "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			path := "/v0/management/usage-stats/errors/timeline"
			if tc.interval != "" {
				q := url.Values{}
				q.Set("interval", tc.interval)
				path += "?" + q.Encode()
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			r.ServeHTTP(w, req)

			// Without PG, requirePG returns 503 first.
			if w.Code == http.StatusServiceUnavailable {
				return
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// newUninitializedPGHandler wires non-nil but uninitialized PG stores into a
// bare Handler. requirePG only nil-checks the store pointers, so it passes;
// each store method then fails its own `s.db == nil` guard and the handler
// returns 500. This lets the tests exercise handler logic (validation, route
// resolution) without a live database — and it distinguishes the static routes
// (500 from the store call) from a shadowed :id match (400 from ParseInt).
func newUninitializedPGHandler() *Handler {
	h := &Handler{}
	h.mu.Lock()
	h.pgAPIKeys = new(store.APIKeyStore)
	h.pgUsage = new(store.UsageStore)
	h.mu.Unlock()
	return h
}

// TestErrorDiagnosticsRouteOrdering proves that the static path segments
// (summary/groups/timeline) are matched BEFORE the /errors/:id wildcard.
//
// Discriminator: with an uninitialized-but-non-nil usage store wired,
//   - the static handlers pass requirePG and then fail inside the store call → 500
//   - GetUsageError (the :id handler) would try ParseInt("summary") → 400
//
// So a 500 proves the static route was selected; a 400 would prove the :id
// route shadowed it. This mirrors the exact route shapes registered in
// internal/api/server_management.go.
func TestErrorDiagnosticsRouteOrdering(t *testing.T) {
	h := newUninitializedPGHandler()
	r := errorDiagnosticsRouter(h)

	cases := []struct {
		path string
		// want is the status the static handler produces when reached. For
		// summary/groups/timeline the store call errors (no db) → 500.
		want int
	}{
		{"/v0/management/usage-stats/errors/summary", http.StatusInternalServerError},
		{"/v0/management/usage-stats/errors/groups", http.StatusInternalServerError},
		{"/v0/management/usage-stats/errors/timeline", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s: status = %d; want %d. A 400 here means the :id "+
					"route shadowed the static route (it parsed the segment as an int). body=%s",
					tc.path, w.Code, tc.want, w.Body.String())
			}
		})
	}

	// Sanity check the discriminator itself: the :id route on a non-numeric
	// segment returns 400, confirming the two statuses are distinguishable.
	t.Run("id route on non-numeric segment", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats/errors/notanumber", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400 (id parse failure)", w.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// PG-backed integration tests (require PGSTORE_TEST_DSN).
// ---------------------------------------------------------------------------

// newTestErrorDiagnosticsHandler opens the PG store and wires it into a bare
// Handler so requirePG passes. Skips when PGSTORE_TEST_DSN is unset.
func newTestErrorDiagnosticsHandler(t *testing.T, schema string) (*Handler, *store.PostgresStore) {
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
	for _, table := range []string{pg.UsageErrorsTable(), pg.APIKeysTable()} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			_ = err
		}
	}
	h := &Handler{}
	h.mu.Lock()
	// requirePG nil-checks BOTH pgAPIKeys and pgUsage, so both must be wired for
	// the PG-backed routes to pass the gate. Mirrors newTestEventBodiesHandler.
	h.pgAPIKeys = store.NewAPIKeyStore(pg)
	h.pgUsage = store.NewUsageStore(pg)
	h.mu.Unlock()
	return h, pg
}

// TestErrorDiagnosticsWithPG runs the full assertions against a real PG
// instance. Skipped without PGSTORE_TEST_DSN.
func TestErrorDiagnosticsWithPG(t *testing.T) {
	h, pg := newTestErrorDiagnosticsHandler(t, "err_diag_full")
	defer pg.Close()
	ctx := context.Background()
	usage := h.pgUsage

	// Insert a few test error rows with known properties.
	now := time.Now().UTC()
	rows := []store.UsageError{
		{
			RequestID:      "req-1",
			Provider:       "openai",
			Model:          "gpt-4",
			FailStatusCode: 429,
			ErrorMessage:   "rate limit exceeded",
			ErrorClass:     "rate_limit",
			RequestedAt:    now.Add(-3 * time.Hour),
		},
		{
			RequestID:      "req-2",
			Provider:       "openai",
			Model:          "gpt-4",
			FailStatusCode: 429,
			ErrorMessage:   "rate limit exceeded again",
			ErrorClass:     "rate_limit",
			RequestedAt:    now.Add(-2 * time.Hour),
		},
		{
			RequestID:      "req-3",
			Provider:       "anthropic",
			Model:          "claude-3",
			FailStatusCode: 400,
			ErrorMessage:   "invalid prompt",
			ErrorClass:     "invalid_request",
			RequestedAt:    now.Add(-1 * time.Hour),
		},
	}
	for _, r := range rows {
		if err := usage.InsertError(ctx, r); err != nil {
			t.Fatalf("insert error: %v", err)
		}
	}

	r := errorDiagnosticsRouter(h)

	t.Run("summary", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats/errors/summary", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
		}
		var s store.ErrorSummary
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
		}
		if s.Total != 3 {
			t.Errorf("total = %d; want 3", s.Total)
		}
		if len(s.ByClass) != 2 {
			t.Errorf("len(by_class) = %d; want 2; got=%+v", len(s.ByClass), s.ByClass)
		}
		if len(s.ByProvider) != 2 {
			t.Errorf("len(by_provider) = %d; want 2", len(s.ByProvider))
		}
		if len(s.TopModels) != 2 {
			t.Errorf("len(top_models) = %d; want 2", len(s.TopModels))
		}
	})

	t.Run("groups default", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats/errors/groups", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			GroupBy string          `json:"group_by"`
			Groups  json.RawMessage `json:"groups"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
		}
		if resp.GroupBy != "class" {
			t.Errorf("group_by = %q; want 'class'", resp.GroupBy)
		}
		if resp.Groups == nil {
			t.Errorf("groups is nil")
		}
	})

	t.Run("groups invalid group_by", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/v0/management/usage-stats/errors/groups?group_by=bogus", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
		}
		// Verify it's our validation message, not a random 400.
		if !strings.Contains(w.Body.String(), "group_by must be one of") {
			t.Errorf("body missing validation message: %s", w.Body.String())
		}
	})

	t.Run("timeline", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/v0/management/usage-stats/errors/timeline?interval=hour", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Interval string          `json:"interval"`
			Series   json.RawMessage `json:"series"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
		}
		if resp.Interval != "hour" {
			t.Errorf("interval = %q; want 'hour'", resp.Interval)
		}
		if resp.Series == nil {
			t.Errorf("series is nil")
		}
	})

	t.Run("timeline invalid interval", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/v0/management/usage-stats/errors/timeline?interval=bogus", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400; body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "interval must be one of") {
			t.Errorf("body missing validation message: %s", w.Body.String())
		}
	})

	t.Run("groups valid group_by provider", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/v0/management/usage-stats/errors/groups?group_by=provider", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200; body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			GroupBy string          `json:"group_by"`
			Groups  json.RawMessage `json:"groups"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
		}
		if resp.GroupBy != "provider" {
			t.Errorf("group_by = %q; want 'provider'", resp.GroupBy)
		}
	})
}
