package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// buildAutoRouterStatsHandler opens a fresh PostgresStore against
// PGSTORE_TEST_DSN (skipping when unset), cleans the usage tables, and wires a
// management Handler with that store so requirePG resolves. The billing events
// are seeded by the caller via the returned UsageStore.
func buildAutoRouterStatsHandler(t *testing.T, schema string) (*Handler, *store.UsageStore) {
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
		pg.APIKeysTable(),
		pg.UsageEventsTable(),
		pg.UsageErrorsTable(),
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	apiKeys := store.NewAPIKeyStore(pg)
	usage := store.NewUsageStore(pg)
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	h.SetPostgresStores(apiKeys, usage, nil, nil, nil)
	return h, usage
}

func newAutoRouterStatsRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/auto-routers/stats", h.GetAutoRouterStats)
	g.GET("/auto-routers/:id", h.GetAutoRouter)
	return r
}

// TestGetAutoRouterStatsRequiresRouterID verifies that the stats endpoint
// rejects a request without router_id (before hitting the store).
func TestGetAutoRouterStatsRequiresRouterID(t *testing.T) {
	h, _ := buildAutoRouterStatsHandler(t, "ar_stats_no_router_id")
	r := newAutoRouterStatsRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auto-routers/stats", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Error.Type != "invalid_request" {
		t.Fatalf("error type = %q, want invalid_request", body.Error.Type)
	}
}

// TestGetAutoRouterTierStatsRoute seeds routed usage events and verifies the
// default top=tier payload returns the per-tier aggregates.
func TestGetAutoRouterTierStatsRoute(t *testing.T) {
	h, usage := buildAutoRouterStatsHandler(t, "ar_stats_tier")
	ctx := context.Background()
	now := time.Now().UTC()
	for _, e := range []store.UsageEvent{
		{Provider: "p", Model: "m1", Tier: "simple", RouterID: "router:smart", RequestedAt: now, TotalTokens: 100, CostUSD: 1.0},
		{Provider: "p", Model: "m2", Tier: "complex", RouterID: "router:smart", RequestedAt: now, TotalTokens: 400, CostUSD: 4.0},
		{Provider: "p", Model: "m3", Tier: "simple", RouterID: "router:other", RequestedAt: now, TotalTokens: 999, CostUSD: 99.0},
	} {
		if err := usage.InsertEvent(ctx, e); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	r := newAutoRouterStatsRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auto-routers/stats?router_id=router:smart", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Tiers []store.AutoRouterTierStat `json:"tiers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Tiers) != 4 {
		t.Fatalf("tiers = %d, want 4 canonical tiers: %+v", len(body.Tiers), body.Tiers)
	}
	byTier := map[string]store.AutoRouterTierStat{}
	for _, st := range body.Tiers {
		byTier[st.Tier] = st
	}
	if st := byTier["simple"]; st.RequestCount != 1 || st.TotalTokens != 100 || st.CostUSD != 1.0 {
		t.Errorf("simple = %+v; want 1/100/1.0", st)
	}
	if st := byTier["complex"]; st.RequestCount != 1 || st.TotalTokens != 400 || st.CostUSD != 4.0 {
		t.Errorf("complex = %+v; want 1/400/4.0", st)
	}
}

// TestGetAutoRouterModelStatsRoute verifies the top=model payload is ordered
// by cost descending and lives under the "models" key.
func TestGetAutoRouterModelStatsRoute(t *testing.T) {
	h, usage := buildAutoRouterStatsHandler(t, "ar_stats_model")
	ctx := context.Background()
	now := time.Now().UTC()
	for _, e := range []store.UsageEvent{
		{Provider: "p", Model: "m-a", Tier: "simple", RouterID: "router:smart", RequestedAt: now, TotalTokens: 100, CostUSD: 2.0},
		{Provider: "p", Model: "m-a", Tier: "medium", RouterID: "router:smart", RequestedAt: now, TotalTokens: 200, CostUSD: 3.0},
		{Provider: "p", Model: "m-b", Tier: "complex", RouterID: "router:smart", RequestedAt: now, TotalTokens: 300, CostUSD: 3.0},
	} {
		if err := usage.InsertEvent(ctx, e); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	r := newAutoRouterStatsRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auto-routers/stats?router_id=router:smart&top=model", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Models []store.AutoRouterModelStat `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body.Models) != 2 {
		t.Fatalf("models = %d, want 2: %+v", len(body.Models), body.Models)
	}
	// Ordered by cost DESC: m-a (5), m-b (3).
	if body.Models[0].Model != "m-a" || body.Models[1].Model != "m-b" {
		t.Fatalf("model order = %s,%s; want m-a,m-b", body.Models[0].Model, body.Models[1].Model)
	}
	if body.Models[0].RequestCount != 2 || body.Models[0].CostUSD != 5.0 || body.Models[0].AvgCostPerReq != 2.5 {
		t.Errorf("m-a = %+v; want 2 req / $5 / $2.5 avg", body.Models[0])
	}
}
