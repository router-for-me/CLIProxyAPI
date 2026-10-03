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

func newProviderPerformanceRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/usage-stats/provider-performance", h.GetProviderPerformance)
	return r
}

// TestGetProviderPerformanceMergesErrorCounts verifies the management endpoint
// folds failed attempts from usage_errors into the success-row error rate, and
// that latency/TPS aggregates are surfaced in the JSON response.
func TestGetProviderPerformanceMergesErrorCounts(t *testing.T) {
	h, usage, _ := buildAutoRouterStatsHandler(t, "provider_perf_handler")
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i, e := range []store.UsageEvent{
		{RequestID: "s1", Provider: "anthropic", Model: "m1", LatencyMs: 1000, TTFTMs: 200, OutputTokens: 800, TotalTokens: 1000, RequestedAt: base},
		{RequestID: "s2", Provider: "anthropic", Model: "m1", LatencyMs: 3000, TTFTMs: 500, OutputTokens: 1500, TotalTokens: 2000, RequestedAt: base.Add(10 * time.Second)},
	} {
		if err := usage.InsertEvent(ctx, e); err != nil {
			t.Fatalf("InsertEvent #%d: %v", i, err)
		}
	}
	if err := usage.InsertError(ctx, store.UsageError{
		RequestID:   "e1",
		Provider:    "anthropic",
		Model:       "m1",
		RequestedAt: base.Add(5 * time.Second),
	}); err != nil {
		t.Fatalf("InsertError: %v", err)
	}

	r := newProviderPerformanceRouter(h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/v0/management/usage-stats/provider-performance?from="+base.Format(time.RFC3339)+"&to="+base.Add(30*time.Second).Format(time.RFC3339), nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Entries []store.ProviderPerformance `json:"entries"`
		GroupBy string                      `json:"group_by"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if body.GroupBy != "provider" {
		t.Errorf("group_by = %q; want provider", body.GroupBy)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("got %d entries; want 1 (%+v)", len(body.Entries), body.Entries)
	}
	got := body.Entries[0]
	if got.Provider != "anthropic" || got.RequestCount != 2 {
		t.Errorf("row = %+v; want anthropic with 2 requests", got)
	}
	if got.ErrorCount != 1 {
		t.Errorf("error_count = %d; want 1", got.ErrorCount)
	}
	if !approxEqualPP(got.ErrorRate, 1.0/3.0) {
		t.Errorf("error_rate = %v; want %v", got.ErrorRate, 1.0/3.0)
	}
	// Generation window = (1000-200)+(3000-500) = 3300ms; 2300 tokens / 3.3s.
	if !approxEqualPP(got.TokensPerSecond, 2300.0/3.3) {
		t.Errorf("tokens_per_second = %v; want %v", got.TokensPerSecond, 2300.0/3.3)
	}
}

// TestGetProviderPerformanceRejectsUnknownGroupBy verifies the handler surfaces
// a 400 rather than a 500 for an unsupported grouping dimension.
func TestGetProviderPerformanceRejectsUnknownGroupBy(t *testing.T) {
	h, _, _ := buildAutoRouterStatsHandler(t, "provider_perf_bad_group")
	r := newProviderPerformanceRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats/provider-performance?group_by=bogus", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func approxEqualPP(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}
