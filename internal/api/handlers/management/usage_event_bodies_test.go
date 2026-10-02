package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// newTestEventBodiesHandler opens a PG-backed APIKeyStore (plus the UsageStore
// requirePG demands) and wires them into a bare Handler so the PG gate passes.
// Skips when PGSTORE_TEST_DSN is unset. Mirrors newTestImportHandler.
func newTestEventBodiesHandler(t *testing.T, schema string) (*Handler, *store.PostgresStore) {
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
	for _, table := range []string{pg.UsageEventsTable(), pg.RequestBodiesTable(), pg.APIKeysTable()} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			_ = err
		}
	}
	h := &Handler{}
	h.mu.Lock()
	h.pgAPIKeys = store.NewAPIKeyStore(pg)
	h.pgUsage = store.NewUsageStore(pg)
	h.mu.Unlock()
	return h, pg
}

// runEventBodies dispatches GET /usage-stats/events/:id/bodies on a minimal
// router that wires only the route under test.
func runEventBodies(t *testing.T, h *Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/usage-stats/events/:id/bodies", h.GetUsageEventBodies)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats/events/"+id+"/bodies", nil)
	r.ServeHTTP(w, req)
	return w
}

func TestUsageEventBodiesUnknownEventReturns404(t *testing.T) {
	h, _ := newTestEventBodiesHandler(t, "event_bodies_404")
	w := runEventBodies(t, h, "999999")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestUsageEventBodiesReturnsCapturedPayloads(t *testing.T) {
	h, pg := newTestEventBodiesHandler(t, "event_bodies_ok")
	ctx := context.Background()
	usage := h.pgUsage
	if err := usage.InsertEvent(ctx, store.UsageEvent{
		RequestID:   "req-x",
		Provider:    "claude",
		Model:       "claude-3",
		RequestedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	var id int64
	if err := pg.DB().QueryRowContext(ctx,
		"SELECT id FROM "+pg.UsageEventsTable()+" WHERE request_id = $1", "req-x").Scan(&id); err != nil {
		t.Fatalf("select event id: %v", err)
	}
	if err := usage.InsertRequestBody(ctx, store.RequestBody{
		RequestID:             "req-x",
		Provider:              "claude",
		UpstreamProviderID:    42,
		ClientRequestHeaders:  `{"Content-Type":["application/json"]}`,
		ClientRequestBody:     `{"prompt":"hi"}`,
		ClientResponseHeaders: `{"Content-Type":["text/event-stream"]}`,
		ClientResponseBody:    `data: hello`,
		UpstreamRequest:       `POST /v1/messages {"prompt":"hi"}`,
		UpstreamResponse:      `{"content":[{"text":"hi"}]}`,
		Truncated:             true,
	}); err != nil {
		t.Fatalf("insert request body: %v", err)
	}

	w := runEventBodies(t, h, strconv.FormatInt(id, 10))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp eventBodiesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Available {
		t.Fatalf("available = false, want true; body=%s", w.Body.String())
	}
	if resp.Provider != "claude" || resp.UpstreamProviderID != 42 || !resp.Truncated {
		t.Errorf("metadata = %+v", resp)
	}
	if resp.CapturedAt == nil {
		t.Errorf("captured_at = nil, want set")
	}
	if resp.ClientRequest == nil || resp.ClientRequest.Body != `{"prompt":"hi"}` {
		t.Fatalf("client_request = %+v, want body object", resp.ClientRequest)
	}
	if got := resp.ClientRequest.Headers["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("client_request.headers = %v, want decoded object", resp.ClientRequest.Headers)
	}
	if resp.ClientResponse == nil || resp.ClientResponse.Body != `data: hello` {
		t.Errorf("client_response = %+v", resp.ClientResponse)
	}
	if resp.UpstreamRequest != `POST /v1/messages {"prompt":"hi"}` {
		t.Errorf("upstream_request = %q", resp.UpstreamRequest)
	}
	if resp.UpstreamResponse != `{"content":[{"text":"hi"}]}` {
		t.Errorf("upstream_response = %q", resp.UpstreamResponse)
	}
}
