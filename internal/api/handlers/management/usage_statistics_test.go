package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

// mockStore implements usagestats.Store for testing.
type mockStore struct {
	summaryResult *usagestats.SummaryResult
	summaryErr    error
	appendErr     error
	schemaErr     error
}

func (m *mockStore) EnsureSchema(_ context.Context) error { return m.schemaErr }
func (m *mockStore) Append(_ context.Context, _ usagestats.Event) error {
	return m.appendErr
}
func (m *mockStore) Summary(_ context.Context, query usagestats.Query) (*usagestats.SummaryResult, error) {
	return m.summaryResult, m.summaryErr
}
func (m *mockStore) Close() error { return nil }

func setupTestHandler(store usagestats.Store) (*Handler, *gin.Engine) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h := NewHandler(cfg, "", nil)
	if store != nil {
		h.usageStatsStore = store
	}
	r := gin.New()
	return h, r
}

func TestGetUsageStatisticsSummary_NoStore(t *testing.T) {
	h, _ := setupTestHandler(nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestGetUsageStatisticsSummary_InvalidFrom(t *testing.T) {
	store := &mockStore{}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?from=not-a-date", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetUsageStatisticsSummary_InvalidGroupBy(t *testing.T) {
	store := &mockStore{}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?group_by=invalid", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "invalid group_by" {
		t.Errorf("error = %q, want 'invalid group_by'", body["error"])
	}
}

func TestGetUsageStatisticsSummary_FromAfterTo(t *testing.T) {
	store := &mockStore{}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?from=2026-06-01&to=2026-05-01", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetUsageStatisticsSummary_InvalidRecentLimit(t *testing.T) {
	store := &mockStore{}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?recent_limit=abc", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetUsageStatisticsSummary_Success(t *testing.T) {
	store := &mockStore{
		summaryResult: &usagestats.SummaryResult{
			From:     "2026-05-01T00:00:00Z",
			To:       "2026-05-27T00:00:00Z",
			GroupBy:  "day",
			Summary:  usagestats.SummaryTotal{Reqs: 10, OK: 8, Fail: 2},
			Groups:   []usagestats.SummaryRow{{Key: "2026-05-27", Reqs: 10}},
			Recent:   nil,
		},
	}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?from=2026-05-01&to=2026-05-27&group_by=day&recent_limit=10", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result usagestats.SummaryResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Summary.Reqs != 10 {
		t.Errorf("requests = %d, want 10", result.Summary.Reqs)
	}
}

func TestGetUsageStatisticsSummary_RecentLimitCapped(t *testing.T) {
	store := &mockStore{
		summaryResult: &usagestats.SummaryResult{
			From:    time.Now().AddDate(0, 0, -1).Format(time.RFC3339),
			To:      time.Now().Format(time.RFC3339),
			GroupBy: "day",
		},
	}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// Request 500, should be capped to 100.
	c.Request = httptest.NewRequest("GET", "/?recent_limit=500", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestGetUsageStatisticsSummary_StoreError(t *testing.T) {
	store := &mockStore{
		summaryErr: fmt.Errorf("db connection lost"),
	}
	h, _ := setupTestHandler(store)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)

	h.GetUsageStatisticsSummary(c)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}
