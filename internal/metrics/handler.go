package metrics

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Handler provides HTTP endpoints for metrics retrieval.
type Handler struct {
	collector *Collector
}

// NewHandler creates a new metrics handler.
func NewHandler(collector *Collector) *Handler {
	return &Handler{collector: collector}
}

// RegisterRoutes registers the metrics endpoint on the given router group.
func (h *Handler) RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/metrics", h.GetMetrics)
}

// GetMetrics handles GET /_korproxy/metrics
func (h *Handler) GetMetrics(c *gin.Context) {
	if h.collector == nil || h.collector.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metrics not available"})
		return
	}

	from, to, err := h.parseTimeRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.collector.Flush()

	metrics, err := h.collector.store.LoadMetrics(from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load metrics"})
		return
	}

	c.JSON(http.StatusOK, metrics)
}

// parseTimeRange extracts from/to from query params with defaults.
func (h *Handler) parseTimeRange(c *gin.Context) (from, to time.Time, err error) {
	now := time.Now().UTC()
	to = now
	from = now.AddDate(0, 0, -7)

	if fromStr := c.Query("from"); fromStr != "" {
		parsed, parseErr := time.Parse(time.RFC3339, fromStr)
		if parseErr != nil {
			parsed, parseErr = time.Parse("2006-01-02", fromStr)
			if parseErr != nil {
				return time.Time{}, time.Time{}, parseErr
			}
		}
		from = parsed.UTC()
	}

	if toStr := c.Query("to"); toStr != "" {
		parsed, parseErr := time.Parse(time.RFC3339, toStr)
		if parseErr != nil {
			parsed, parseErr = time.Parse("2006-01-02", toStr)
			if parseErr != nil {
				return time.Time{}, time.Time{}, parseErr
			}
			parsed = parsed.Add(24*time.Hour - time.Nanosecond)
		}
		to = parsed.UTC()
	}

	return from, to, nil
}
