package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	var snapshot usage.StatisticsSnapshot
	if h != nil && h.usageStats != nil {
		snapshot = h.usageStats.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

// GetTPSAggregates returns average and median TPS since server start.
// It aggregates samples recorded during request processing.
func (h *Handler) GetTPSAggregates(c *gin.Context) {
	// optional query: window=e.g. 5m, 1h, 30s; provider, model for filtering
	windowStr := c.Query("window")
	provider := c.Query("provider")
	model := c.Query("model")
	var (
		snap usage.TPSAggregateSnapshot
		d    time.Duration
		err  error
	)
	if windowStr != "" {
		d, err = time.ParseDuration(windowStr)
		if err != nil || d <= 0 {
			d = 0
		}
	}
	if provider != "" || model != "" {
		snap = usage.GetTPSAggregatesWindowFiltered(d, provider, model)
	} else if d > 0 {
		snap = usage.GetTPSAggregatesWindow(d)
	} else {
		snap = usage.GetTPSAggregates()
	}
	c.JSON(http.StatusOK, gin.H{"tps": snap})
}
