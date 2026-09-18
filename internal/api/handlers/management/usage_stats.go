package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

const usageStatsDateLayout = "2006-01-02"

// GetUsageStats returns persisted usage records aggregated by day, provider,
// model, and account. It reads from local usage-stats/*.jsonl files rather
// than any in-memory queue, so results survive process restarts.
func (h *Handler) GetUsageStats(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	to := time.Now().UTC()
	from := to.AddDate(0, 0, -7)

	if raw := c.Query("from"); raw != "" {
		parsed, errParse := time.Parse(usageStatsDateLayout, raw)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from date, expected YYYY-MM-DD"})
			return
		}
		from = parsed
	}
	if raw := c.Query("to"); raw != "" {
		parsed, errParse := time.Parse(usageStatsDateLayout, raw)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to date, expected YYYY-MM-DD"})
			return
		}
		to = parsed
	}

	stats, err := usagestats.QueryDaily(usagestats.Dir(h.logDirectory()), from, to)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"from":  from.Format(usageStatsDateLayout),
		"to":    to.Format(usageStatsDateLayout),
		"stats": stats,
	})
}
