package metrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func setupTestHandler(t *testing.T) (*Handler, *Collector, *gin.Engine) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	h := NewHandler(c)
	r := gin.New()
	group := r.Group("/_korproxy")
	h.RegisterRoutes(group)

	return h, c, r
}

func TestHandlerGetMetrics(t *testing.T) {
	_, c, r := setupTestHandler(t)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)
	c.RecordSuccess("openai", "gpt-4", "work", routing.RequestTypeCompletion, 200)
	c.RecordError("gemini", "gemini-pro", "default", routing.RequestTypeChat, 100, "timeout")

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp MetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Summary.TotalRequests != 3 {
		t.Errorf("expected TotalRequests=3, got %d", resp.Summary.TotalRequests)
	}
	if resp.Summary.TotalFailures != 1 {
		t.Errorf("expected TotalFailures=1, got %d", resp.Summary.TotalFailures)
	}
}

func TestHandlerGetMetricsWithTimeRange(t *testing.T) {
	_, c, r := setupTestHandler(t)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)
	c.Flush()

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -1).Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")

	req, _ := http.NewRequest("GET", "/_korproxy/metrics?from="+from+"&to="+to, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Summary.TotalRequests != 1 {
		t.Errorf("expected TotalRequests=1, got %d", resp.Summary.TotalRequests)
	}
}

func TestHandlerGetMetricsWithRFC3339(t *testing.T) {
	_, c, r := setupTestHandler(t)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)
	c.Flush()

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -1).Format(time.RFC3339)
	to := now.AddDate(0, 0, 1).Format(time.RFC3339)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics?from="+from+"&to="+to, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
}

func TestHandlerGetMetricsInvalidTimeRange(t *testing.T) {
	_, _, r := setupTestHandler(t)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics?from=invalid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for invalid from, got %d", w.Code)
	}
}

func TestHandlerGetMetricsNoCollector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandler(nil)
	r := gin.New()
	group := r.Group("/_korproxy")
	h.RegisterRoutes(group)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 with nil collector, got %d", w.Code)
	}
}

func TestHandlerResponseSchema(t *testing.T) {
	_, c, r := setupTestHandler(t)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 100)
	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 200)
	c.RecordError("openai", "gpt-4", "work", routing.RequestTypeCompletion, 150, "rate_limit")
	c.Flush()

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Period.From == "" || resp.Period.To == "" {
		t.Error("period should have from and to")
	}

	if len(resp.ByProvider) == 0 {
		t.Error("by_provider should not be empty")
	}
	if claude, ok := resp.ByProvider["claude"]; ok {
		if claude.Requests != 2 {
			t.Errorf("expected claude requests=2, got %d", claude.Requests)
		}
	} else {
		t.Error("expected claude in by_provider")
	}

	if len(resp.ByType) == 0 {
		t.Error("by_type should not be empty")
	}

	if len(resp.ByProfile) == 0 {
		t.Error("by_profile should not be empty")
	}

	if len(resp.Daily) == 0 {
		t.Error("daily should not be empty")
	}
}

func TestHandlerDefaultTimeRange(t *testing.T) {
	_, c, r := setupTestHandler(t)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)
	c.Flush()

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	fromTime, _ := time.Parse(time.RFC3339, resp.Period.From)
	toTime, _ := time.Parse(time.RFC3339, resp.Period.To)

	diff := toTime.Sub(fromTime)
	if diff < 6*24*time.Hour || diff > 8*24*time.Hour {
		t.Errorf("default range should be ~7 days, got %v", diff)
	}
}

func BenchmarkHandlerGetMetrics(b *testing.B) {
	gin.SetMode(gin.TestMode)

	dir := b.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	for i := 0; i < 1000; i++ {
		c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, int64(i))
	}
	c.Flush()

	h := NewHandler(c)
	r := gin.New()
	group := r.Group("/_korproxy")
	h.RegisterRoutes(group)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

func TestNewDailyMetrics(t *testing.T) {
	dm := NewDailyMetrics("2024-01-15")
	if dm.Date != "2024-01-15" {
		t.Errorf("expected date=2024-01-15, got %s", dm.Date)
	}
	if dm.ByProvider == nil {
		t.Error("ByProvider should be initialized")
	}
	if dm.ByType == nil {
		t.Error("ByType should be initialized")
	}
	if dm.ByProfile == nil {
		t.Error("ByProfile should be initialized")
	}
	if dm.Histogram == nil {
		t.Error("Histogram should be initialized")
	}
}

func TestTypesInitialization(t *testing.T) {
	globalCollector = nil
	globalCollectorOnce = sync.Once{}
	
	c := GetCollector()
	if c != nil {
		t.Error("GetCollector should return nil before initialization")
	}
}
