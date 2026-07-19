package management

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// usageStatsQuery captures the supported query parameters for the usage
// aggregate endpoint.
type usageStatsQuery struct {
	APIKeyID  string
	Principal string
	Provider  string
	Model     string
	From      time.Time
	To        time.Time
	GroupBy   string
	Limit     int
}

func parseUsageStatsQuery(c *gin.Context) usageStatsQuery {
	q := usageStatsQuery{
		APIKeyID:  c.Query("api_key_id"),
		Principal: c.Query("api_key_principal"),
		Provider:  c.Query("provider"),
		Model:     c.Query("model"),
		GroupBy:   c.DefaultQuery("group_by", "model"),
	}
	if from := c.Query("from"); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			q.From = t
		}
	}
	if to := c.Query("to"); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			q.To = t
		}
	}
	if limit := c.Query("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 {
			q.Limit = n
		}
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	return q
}

// GetUsageStats handles GET /v0/management/usage-stats. It runs an aggregate
// query over usage_events pre-computed by the PG usage flusher.
func (h *Handler) GetUsageStats(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	aggs, err := usage.SelectAggregate(c.Request.Context(), store.UsageFilter{
		APIKeyID:  q.APIKeyID,
		Principal: q.Principal,
		Provider:  q.Provider,
		Model:     q.Model,
		From:      q.From,
		To:        q.To,
		GroupBy:   q.GroupBy,
		Limit:     q.Limit,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"aggregates": aggs, "group_by": q.GroupBy})
}

// GetUsageSummary handles GET /v0/management/usage-stats/summary?window=hourly|weekly|monthly.
func (h *Handler) GetUsageSummary(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	window := c.DefaultQuery("window", store.WindowTypeHourly)
	switch window {
	case store.WindowTypeHourly, store.WindowTypeWeekly, store.WindowTypeMonthly:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "window must be hourly|weekly|monthly"}})
		return
	}
	apiKeyID := c.Query("api_key_id")
	if apiKeyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "api_key_id is required"}})
		return
	}
	windows, err := usage.ListWindows(c.Request.Context(), apiKeyID, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Filter by window type.
	out := make([]store.UsageWindow, 0, len(windows))
	for _, w := range windows {
		if w.WindowType == window {
			out = append(out, w)
		}
	}
	c.JSON(http.StatusOK, gin.H{"window": window, "api_key_id": apiKeyID, "windows": out})
}

// GetUsageWindows handles GET /v0/management/usage-windows/:api_key_id.
func (h *Handler) GetUsageWindows(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	apiKeyID := c.Param("api_key_id")
	windows, err := usage.ListWindows(c.Request.Context(), apiKeyID, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_key_id": apiKeyID, "windows": windows})
}

// GetModelPricing handles GET /v0/management/models-catalog/:id/pricing.
func (h *Handler) GetModelPricing(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id := c.Param("id")
	p, err := usage.GetPricing(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, p)
}

// PutModelPricing handles PUT /v0/management/models-catalog/:id/pricing.
func (h *Handler) PutModelPricing(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	var p store.Pricing
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	p.ID = c.Param("id")
	if err := usage.UpsertPricing(c.Request.Context(), p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, p)
}

// GetUsageTimeSeries handles GET /v0/management/usage-stats/timeseries.
//
// Query parameters:
//   - interval: "minute" | "hour" (default) | "day"
//   - api_key_id, api_key_principal, provider, model, from, to, limit
//     (same filter semantics as /usage-stats)
//
// Returns one bucket per interval across the filtered window, sorted
// ascending. Buckets with zero activity are omitted — the dashboard can
// pad client-side if it needs a continuous axis.
func (h *Handler) GetUsageTimeSeries(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	ts, err := usage.SelectTimeSeries(c.Request.Context(), filter, c.DefaultQuery("interval", "hour"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"points": ts, "interval": c.DefaultQuery("interval", "hour")})
}

// GetUsageTop handles GET /v0/management/usage-stats/top.
//
// Required query parameters:
//   - dimension: "model" | "provider" | "api_key_id" | "api_key_principal"
//   - metric:    "request_count" (default) | "total_tokens" | "cost_usd"
//   - limit:     default 10, max 500
//
// Plus the usual filter set (api_key_id, provider, model, from, to).
func (h *Handler) GetUsageTop(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	dimension := c.DefaultQuery("dimension", "model")
	metric := c.DefaultQuery("metric", "request_count")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if limit <= 0 {
		limit = 10
	}
	q := parseUsageStatsQuery(c)
	q.Limit = limit
	filter := filterFromQuery(q)
	entries, err := usage.SelectTop(c.Request.Context(), filter, dimension, metric, limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"entries":   entries,
		"dimension": dimension,
		"metric":    metric,
	})
}

// GetUsageTotals handles GET /v0/management/usage-stats/totals.
//
// Returns the rolled-up KPI card values for the supplied filter: total
// request count, failed count, token sums, and cost USD. Cheap query (no
// GROUP BY), suitable for the dashboard's top-of-page summary tiles.
func (h *Handler) GetUsageTotals(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	totals, err := usage.SelectTotals(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Compute derived metrics for the dashboard.
	var failureRate float64
	if totals.RequestCount > 0 {
		failureRate = float64(totals.FailedCount) / float64(totals.RequestCount) * 100
	}
	c.JSON(http.StatusOK, gin.H{
		"totals":       totals,
		"failure_rate": failureRate,
	})
}

// filterFromQuery converts the parsed query struct into a store.UsageFilter,
// trimming empty values so they are not added as WHERE clauses.
func filterFromQuery(q usageStatsQuery) store.UsageFilter {
	f := store.UsageFilter{
		APIKeyID:  q.APIKeyID,
		Principal: q.Principal,
		Provider:  q.Provider,
		Model:     q.Model,
		From:      q.From,
		To:        q.To,
		GroupBy:   q.GroupBy,
		Limit:     q.Limit,
	}
	return f
}
