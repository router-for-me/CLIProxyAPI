package management

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CostAggregationRequest specifies parameters for cost aggregation
type CostAggregationRequest struct {
	// StartTime is the start of the aggregation period
	StartTime time.Time `json:"start_time"`
	// EndTime is the end of the aggregation period
	EndTime time.Time `json:"end_time"`
	// Granularity is the aggregation granularity: hour, day, week, month
	Granularity string `json:"granularity"`
	// GroupBy is the grouping: model, provider, client
	GroupBy string `json:"group_by"`
	// FilterProvider limits to specific provider
	FilterProvider string `json:"filter_provider,omitempty"`
	// FilterModel limits to specific model
	FilterModel string `json:"filter_model,omitempty"`
}

// CostAggregationResponse contains aggregated cost data
type CostAggregationResponse struct {
	StartTime   time.Time      `json:"start_time"`
	EndTime     time.Time      `json:"end_time"`
	Granularity string         `json:"granularity"`
	GroupBy     string         `json:"group_by"`
	TotalCost   float64        `json:"total_cost"`
	TotalTokens int64          `json:"total_tokens"`
	Groups      []CostGroup    `json:"groups"`
	TimeSeries  []TimeSeriesPoint `json:"time_series,omitempty"`
}

// CostGroup represents a grouped cost entry
type CostGroup struct {
	Key         string  `json:"key"` // model/provider/client ID
	Cost        float64 `json:"cost"`
	InputTokens int64   `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Requests     int64   `json:"requests"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// TimeSeriesPoint represents a point in time series data
type TimeSeriesPoint struct {
	Timestamp    time.Time `json:"timestamp"`
	Cost        float64   `json:"cost"`
	InputTokens int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	Requests    int64     `json:"requests"`
}

// UsageAnalytics provides usage analytics functionality
type UsageAnalytics struct {
	mu            sync.RWMutex
	records       []UsageRecord
	maxRecords    int
}

// UsageRecord represents a single usage record
type UsageRecord struct {
	Timestamp    time.Time
	ModelID      string
	Provider     string
	ClientID     string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Cost         float64
	LatencyMs    int
	Success      bool
}

// NewUsageAnalytics creates a new UsageAnalytics instance
func NewUsageAnalytics() *UsageAnalytics {
	return &UsageAnalytics{
		records:    make([]UsageRecord, 0),
		maxRecords: 1000000, // Keep 1M records in memory
	}
}

// RecordUsage records a usage event
func (u *UsageAnalytics) RecordUsage(ctx context.Context, record UsageRecord) {
	u.mu.Lock()
	defer u.mu.Unlock()

	record.Timestamp = time.Now()
	u.records = append(u.records, record)

	// Trim if over limit
	if len(u.records) > u.maxRecords {
		u.records = u.records[len(u.records)-u.maxRecords:]
	}
}

// GetCostAggregation returns aggregated cost data
func (u *UsageAnalytics) GetCostAggregation(ctx context.Context, req *CostAggregationRequest) (*CostAggregationResponse, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()

	if req.StartTime.IsZero() {
		req.StartTime = time.Now().Add(-24 * time.Hour)
	}
	if req.EndTime.IsZero() {
		req.EndTime = time.Now()
	}
	if req.Granularity == "" {
		req.Granularity = "day"
	}
	if req.GroupBy == "" {
		req.GroupBy = "model"
	}

	// Filter records by time range
	var filtered []UsageRecord
	for _, r := range u.records {
		if r.Timestamp.After(req.StartTime) && r.Timestamp.Before(req.EndTime) {
			if req.FilterProvider != "" && r.Provider != req.FilterProvider {
				continue
			}
			if req.FilterModel != "" && r.ModelID != req.FilterModel {
				continue
			}
			filtered = append(filtered, r)
		}
	}

	// Aggregate by group
	groups := make(map[string]*CostGroup)
	var totalCost float64
	var totalTokens int64

	for _, r := range filtered {
		var key string
		switch req.GroupBy {
		case "model":
			key = r.ModelID
		case "provider":
			key = r.Provider
		case "client":
			key = r.ClientID
		default:
			key = r.ModelID
		}

		if _, ok := groups[key]; !ok {
			groups[key] = &CostGroup{Key: key}
		}

		g := groups[key]
		g.Cost += r.Cost
		g.InputTokens += int64(r.InputTokens)
		g.OutputTokens += int64(r.OutputTokens)
		g.Requests++
		if r.LatencyMs > 0 {
			g.AvgLatencyMs = (g.AvgLatencyMs*float64(g.Requests-1) + float64(r.LatencyMs)) / float64(g.Requests)
		}

		totalCost += r.Cost
		totalTokens += int64(r.TotalTokens)
	}

	// Convert to slice
	result := make([]CostGroup, 0, len(groups))
	for _, g := range groups {
		result = append(result, *g)
	}

	// Generate time series
	var timeSeries []TimeSeriesPoint
	if len(filtered) > 0 {
		timeSeries = u.generateTimeSeries(filtered, req.Granularity)
	}

	return &CostAggregationResponse{
		StartTime:   req.StartTime,
		EndTime:     req.EndTime,
		Granularity: req.Granularity,
		GroupBy:     req.GroupBy,
		TotalCost:   totalCost,
		TotalTokens: totalTokens,
		Groups:      result,
		TimeSeries:  timeSeries,
	}, nil
}

// generateTimeSeries creates time series data from records
func (u *UsageAnalytics) generateTimeSeries(records []UsageRecord, granularity string) []TimeSeriesPoint {
	// Determine bucket size
	var bucketSize time.Duration
	switch granularity {
	case "hour":
		bucketSize = time.Hour
	case "day":
		bucketSize = 24 * time.Hour
	case "week":
		bucketSize = 7 * 24 * time.Hour
	case "month":
		bucketSize = 30 * 24 * time.Hour
	default:
		bucketSize = 24 * time.Hour
	}

	// Group by time buckets
	buckets := make(map[int64]*TimeSeriesPoint)
	for _, r := range records {
		bucket := r.Timestamp.Unix() / int64(bucketSize.Seconds())
		if _, ok := buckets[bucket]; !ok {
			buckets[bucket] = &TimeSeriesPoint{
				Timestamp: time.Unix(bucket*int64(bucketSize.Seconds()), 0),
			}
		}
		b := buckets[bucket]
		b.Cost += r.Cost
		b.InputTokens += int64(r.InputTokens)
		b.OutputTokens += int64(r.OutputTokens)
		b.Requests++
	}

	// Convert to slice and sort
	result := make([]TimeSeriesPoint, 0, len(buckets))
	for _, p := range buckets {
		result = append(result, *p)
	}

	// Sort by timestamp
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Timestamp.Before(result[i].Timestamp) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result
}

// GetTopModels returns top models by cost
func (u *UsageAnalytics) GetTopModels(ctx context.Context, limit int, timeRange time.Duration) ([]CostGroup, error) {
	req := &CostAggregationRequest{
		StartTime:  time.Now().Add(-timeRange),
		EndTime:    time.Now(),
		Granularity: "day",
		GroupBy:    "model",
	}

	resp, err := u.GetCostAggregation(ctx, req)
	if err != nil {
		return nil, err
	}

	// Sort by cost descending
	groups := resp.Groups
	for i := 0; i < len(groups)-1; i++ {
		for j := i + 1; j < len(groups); j++ {
			if groups[j].Cost > groups[i].Cost {
				groups[i], groups[j] = groups[j], groups[i]
			}
		}
	}

	if len(groups) > limit {
		groups = groups[:limit]
	}

	return groups, nil
}

// GetProviderBreakdown returns cost breakdown by provider
func (u *UsageAnalytics) GetProviderBreakdown(ctx context.Context, timeRange time.Duration) (map[string]float64, error) {
	req := &CostAggregationRequest{
		StartTime:  time.Now().Add(-timeRange),
		EndTime:    time.Now(),
		Granularity: "day",
		GroupBy:    "provider",
	}

	resp, err := u.GetCostAggregation(ctx, req)
	if err != nil {
		return nil, err
	}

	result := make(map[string]float64)
	for _, g := range resp.Groups {
		result[g.Key] = g.Cost
	}

	return result, nil
}

// GetDailyTrend returns daily cost trend
func (u *UsageAnalytics) GetDailyTrend(ctx context.Context, days int) ([]TimeSeriesPoint, error) {
	req := &CostAggregationRequest{
		StartTime:  time.Now().Add(time.Duration(-days) * 24 * time.Hour),
		EndTime:    time.Now(),
		Granularity: "day",
		GroupBy:    "model",
	}

	resp, err := u.GetCostAggregation(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.TimeSeries, nil
}

// GetCostSummary returns a summary of costs
func (u *UsageAnalytics) GetCostSummary(ctx context.Context, timeRange time.Duration) (map[string]interface{}, error) {
	req := &CostAggregationRequest{
		StartTime:  time.Now().Add(-timeRange),
		EndTime:    time.Now(),
		Granularity: "day",
		GroupBy:    "model",
	}

	resp, err := u.GetCostAggregation(ctx, req)
	if err != nil {
		return nil, err
	}

	// Calculate additional metrics
	var totalRequests int64
	var totalInputTokens, totalOutputTokens int64
	for _, g := range resp.Groups {
		totalRequests += g.Requests
		totalInputTokens += g.InputTokens
		totalOutputTokens += g.OutputTokens
	}

	avgCostPerRequest := 0.0
	if totalRequests > 0 {
		avgCostPerRequest = resp.TotalCost / float64(totalRequests)
	}

	return map[string]interface{}{
		"total_cost":           resp.TotalCost,
		"total_tokens":         resp.TotalTokens,
		"total_requests":       totalRequests,
		"total_input_tokens":    totalInputTokens,
		"total_output_tokens":  totalOutputTokens,
		"avg_cost_per_request": avgCostPerRequest,
		"time_range":           timeRange.String(),
		"period_start":         req.StartTime,
		"period_end":           req.EndTime,
	}, nil
}

// Example UsageAnalyticsHandler
type UsageAnalyticsHandler struct {
	analytics *UsageAnalytics
}

// NewUsageAnalyticsHandler creates a new handler
func NewUsageAnalyticsHandler() *UsageAnalyticsHandler {
	return &UsageAnalyticsHandler{
		analytics: NewUsageAnalytics(),
	}
}

// GETCostSummary handles GET /v1/analytics/costs
func (h *UsageAnalyticsHandler) GETCostSummary(c *gin.Context) {
	timeRange := c.DefaultQuery("timeRange", "24h")
	
	duration, err := time.ParseDuration(timeRange)
	if err != nil {
		duration = 24 * time.Hour
	}

	summary, err := h.analytics.GetCostSummary(c.Request.Context(), duration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// GETCostAggregation handles GET /v1/analytics/costs/breakdown
func (h *UsageAnalyticsHandler) GETCostAggregation(c *gin.Context) {
	var req CostAggregationRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.analytics.GetCostAggregation(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GETTopModels handles GET /v1/analytics/top-models
func (h *UsageAnalyticsHandler) GETTopModels(c *gin.Context) {
	limit := 10
	timeRange := c.DefaultQuery("timeRange", "24h")
	
	duration, err := time.ParseDuration(timeRange)
	if err != nil {
		duration = 24 * time.Hour
	}

	topModels, err := h.analytics.GetTopModels(c.Request.Context(), limit, duration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"limit":      limit,
		"time_range": timeRange,
		"top_models": topModels,
	})
}

// GETProviderBreakdown handles GET /v1/analytics/provider-breakdown
func (h *UsageAnalyticsHandler) GETProviderBreakdown(c *gin.Context) {
	timeRange := c.DefaultQuery("timeRange", "24h")
	
	duration, err := time.ParseDuration(timeRange)
	if err != nil {
		duration = 24 * time.Hour
	}

	breakdown, err := h.analytics.GetProviderBreakdown(c.Request.Context(), duration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"time_range": timeRange,
		"breakdown":  breakdown,
	})
}

// GETDailyTrend handles GET /v1/analytics/daily-trend
func (h *UsageAnalyticsHandler) GETDailyTrend(c *gin.Context) {
	days := 7
	fmt.Sscanf(c.DefaultQuery("days", "7"), "%d", &days)

	trend, err := h.analytics.GetDailyTrend(c.Request.Context(), days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"days":  days,
		"trend": trend,
	})
}
