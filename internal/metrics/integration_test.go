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

// Integration tests that verify the full metrics pipeline:
// Collector -> Store -> Handler -> Response

func TestMetricsIntegration_FullPipeline(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. Set up infrastructure
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	// 2. Record various request types
	requests := []struct {
		provider    string
		model       string
		profile     string
		requestType routing.RequestType
		latencyMs   int64
		errorType   string
	}{
		{"claude", "claude-3-opus", "default", routing.RequestTypeChat, 150, ""},
		{"claude", "claude-3-sonnet", "default", routing.RequestTypeChat, 100, ""},
		{"openai", "gpt-4", "work", routing.RequestTypeCompletion, 200, ""},
		{"openai", "gpt-4", "work", routing.RequestTypeChat, 180, ""},
		{"gemini", "gemini-pro", "default", routing.RequestTypeChat, 120, "timeout"},
		{"claude", "claude-3-opus", "default", routing.RequestTypeEmbedding, 50, ""},
		{"openai", "text-embedding-ada", "work", routing.RequestTypeEmbedding, 30, "rate_limit"},
	}

	for _, r := range requests {
		if r.errorType != "" {
			c.RecordError(r.provider, r.model, r.profile, r.requestType, r.latencyMs, r.errorType)
		} else {
			c.RecordSuccess(r.provider, r.model, r.profile, r.requestType, r.latencyMs)
		}
	}

	// 3. Set up HTTP handler
	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	// 4. Query metrics
	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp MetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 5. Verify summary
	if resp.Summary.TotalRequests != 7 {
		t.Errorf("TotalRequests = %d, want 7", resp.Summary.TotalRequests)
	}
	if resp.Summary.TotalFailures != 2 {
		t.Errorf("TotalFailures = %d, want 2", resp.Summary.TotalFailures)
	}

	// 6. Verify by provider breakdown
	if claude, ok := resp.ByProvider["claude"]; ok {
		if claude.Requests != 3 {
			t.Errorf("claude requests = %d, want 3", claude.Requests)
		}
	} else {
		t.Error("expected claude in by_provider")
	}

	if openai, ok := resp.ByProvider["openai"]; ok {
		if openai.Requests != 3 {
			t.Errorf("openai requests = %d, want 3", openai.Requests)
		}
		if openai.Failures != 1 {
			t.Errorf("openai failures = %d, want 1", openai.Failures)
		}
	} else {
		t.Error("expected openai in by_provider")
	}

	// 7. Verify by type breakdown
	if chat, ok := resp.ByType["chat"]; ok {
		if chat.Requests != 4 {
			t.Errorf("chat requests = %d, want 4", chat.Requests)
		}
	} else {
		t.Error("expected chat in by_type")
	}

	if embedding, ok := resp.ByType["embedding"]; ok {
		if embedding.Requests != 2 {
			t.Errorf("embedding requests = %d, want 2", embedding.Requests)
		}
	} else {
		t.Error("expected embedding in by_type")
	}

	// 8. Verify by profile breakdown
	if defaultProfile, ok := resp.ByProfile["default"]; ok {
		if defaultProfile.Requests != 4 {
			t.Errorf("default profile requests = %d, want 4", defaultProfile.Requests)
		}
	} else {
		t.Error("expected default in by_profile")
	}

	if workProfile, ok := resp.ByProfile["work"]; ok {
		if workProfile.Requests != 3 {
			t.Errorf("work profile requests = %d, want 3", workProfile.Requests)
		}
	} else {
		t.Error("expected work in by_profile")
	}
}

func TestMetricsIntegration_LatencyHistogram(t *testing.T) {
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

	// Record requests with known latencies for histogram testing
	latencies := []int64{25, 75, 150, 250, 750, 1500, 3000}
	for _, lat := range latencies {
		c.RecordSuccess("test", "model", "default", routing.RequestTypeChat, lat)
	}
	c.Flush()

	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Verify average latency is reasonable (should be ~821ms average)
	if resp.Summary.AvgLatencyMs < 500 || resp.Summary.AvgLatencyMs > 1200 {
		t.Errorf("AvgLatencyMs = %.2f, expected ~821", resp.Summary.AvgLatencyMs)
	}

	// Verify daily data exists
	if len(resp.Daily) > 0 {
		day := resp.Daily[0]
		if day.Requests != 7 {
			t.Errorf("daily requests = %d, want 7", day.Requests)
		}
		// Avg latency should be recorded
		if day.AvgLatencyMs < 500 || day.AvgLatencyMs > 1200 {
			t.Errorf("daily AvgLatencyMs = %.2f, expected ~821", day.AvgLatencyMs)
		}
	}
}

func TestMetricsIntegration_ErrorTypes(t *testing.T) {
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

	// Record different error types
	c.RecordError("p1", "m1", "default", routing.RequestTypeChat, 100, "rate_limit")
	c.RecordError("p1", "m1", "default", routing.RequestTypeChat, 100, "rate_limit")
	c.RecordError("p2", "m2", "default", routing.RequestTypeChat, 100, "timeout")
	c.RecordError("p3", "m3", "default", routing.RequestTypeChat, 100, "server_error")
	c.Flush()

	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Summary.TotalFailures != 4 {
		t.Errorf("TotalFailures = %d, want 4", resp.Summary.TotalFailures)
	}

	// Verify errors are tracked by provider
	if p1, ok := resp.ByProvider["p1"]; ok {
		if p1.Failures != 2 {
			t.Errorf("p1 failures = %d, want 2", p1.Failures)
		}
	}
}

func TestMetricsIntegration_ConcurrentWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	store, _ := NewStore(dir)

	// Use large buffer and high flush threshold to avoid auto-flush during test
	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 5000),
		flushSize:   5000, // Won't trigger auto-flush with 1000 records
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	// Concurrent writes
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				c.RecordSuccess("provider", "model", "profile", routing.RequestTypeChat, int64(n*10+j))
			}
		}(i)
	}
	wg.Wait()
	c.Flush()

	// Query and verify
	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Should have recorded all 1000 requests
	if resp.Summary.TotalRequests != 1000 {
		t.Errorf("TotalRequests = %d, want 1000", resp.Summary.TotalRequests)
	}
}

func TestMetricsIntegration_DateRange(t *testing.T) {
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

	// Record some metrics
	for i := 0; i < 10; i++ {
		c.RecordSuccess("test", "model", "default", routing.RequestTypeChat, 100)
	}
	c.Flush()

	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	// Query with narrow date range (today only)
	now := time.Now().UTC()
	from := now.Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")

	req, _ := http.NewRequest("GET", "/_korproxy/metrics?from="+from+"&to="+to, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	// All records should be in today's range
	if resp.Summary.TotalRequests != 10 {
		t.Errorf("TotalRequests = %d, want 10", resp.Summary.TotalRequests)
	}

	// Query with past date range (should be empty)
	pastFrom := now.AddDate(0, 0, -30).Format("2006-01-02")
	pastTo := now.AddDate(0, 0, -20).Format("2006-01-02")

	req2, _ := http.NewRequest("GET", "/_korproxy/metrics?from="+pastFrom+"&to="+pastTo, nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	var resp2 MetricsResponse
	json.Unmarshal(w2.Body.Bytes(), &resp2)

	if resp2.Summary.TotalRequests != 0 {
		t.Errorf("TotalRequests for past range = %d, want 0", resp2.Summary.TotalRequests)
	}
}

func TestMetricsIntegration_EmptyMetrics(t *testing.T) {
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

	// Don't record any metrics

	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp MetricsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Should have valid empty response
	if resp.Summary.TotalRequests != 0 {
		t.Errorf("TotalRequests = %d, want 0", resp.Summary.TotalRequests)
	}
	if resp.Summary.TotalFailures != 0 {
		t.Errorf("TotalFailures = %d, want 0", resp.Summary.TotalFailures)
	}
}

func BenchmarkMetricsIntegration_Pipeline(b *testing.B) {
	gin.SetMode(gin.TestMode)

	dir := b.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 10000),
		flushSize:   5000,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	// Pre-populate with data
	for i := 0; i < 10000; i++ {
		c.RecordSuccess("provider", "model", "profile", routing.RequestTypeChat, int64(i%1000))
	}
	c.Flush()

	h := NewHandler(c)
	router := gin.New()
	group := router.Group("/_korproxy")
	h.RegisterRoutes(group)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", "/_korproxy/metrics", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}
