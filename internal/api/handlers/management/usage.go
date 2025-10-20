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
    c.JSON(http.StatusOK, gin.H{"usage": snapshot})
}

// GetTPSAggregates returns average and median TPS since server start.
// It aggregates samples recorded during request processing.
func (h *Handler) GetTPSAggregates(c *gin.Context) {
    // optional query: window=e.g. 5m, 1h, 30s
    windowStr := c.Query("window")
    var snap usage.TPSAggregateSnapshot
    if windowStr == "" {
        snap = usage.GetTPSAggregates()
    } else {
        if d, err := time.ParseDuration(windowStr); err == nil && d > 0 {
            snap = usage.GetTPSAggregatesWindow(d)
        } else {
            // invalid duration → fall back to full aggregates
            snap = usage.GetTPSAggregates()
        }
    }
    c.JSON(http.StatusOK, gin.H{"tps": snap})
}
