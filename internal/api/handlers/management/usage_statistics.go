package management

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

const (
	defaultRecentLimit = 0
	maxRecentLimit     = 100
)

// GetUsageStatisticsSummary handles GET /v0/management/usage-statistics/summary.
func (h *Handler) GetUsageStatisticsSummary(c *gin.Context) {
	if h == nil || h.usageStatsStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "usage statistics unavailable",
		})
		return
	}

	// Parse from.
	fromStr := strings.TrimSpace(c.Query("from"))
	from, err := usagestats.ParseTime(fromStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from", "message": err.Error()})
		return
	}

	// Parse to.
	toStr := strings.TrimSpace(c.Query("to"))
	to, err := usagestats.ParseTime(toStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to", "message": err.Error()})
		return
	}

	// Default to = now.
	if to.IsZero() {
		to = time.Now()
	}
	// Default from = 30 days ago.
	if from.IsZero() {
		from = to.AddDate(0, 0, -30)
	}
	if !from.Before(to) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid range", "message": "from must be before to"})
		return
	}

	// Parse group_by.
	groupByStr := strings.TrimSpace(c.Query("group_by"))
	groupBy, ok := usagestats.ParseGroupBy(groupByStr)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid group_by",
			"message": fmt.Sprintf("must be one of: day, provider, model, api_key, auth, call_type"),
		})
		return
	}

	// Parse recent_limit.
	recentLimit := defaultRecentLimit
	if rlStr := strings.TrimSpace(c.Query("recent_limit")); rlStr != "" {
		rl, err := strconv.Atoi(rlStr)
		if err != nil || rl < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid recent_limit", "message": "must be a non-negative integer"})
			return
		}
		if rl > maxRecentLimit {
			rl = maxRecentLimit
		}
		recentLimit = rl
	}

	result, err := h.usageStatsStore.Summary(c.Request.Context(), usagestats.Query{
		From:        from,
		To:          to,
		GroupBy:     groupBy,
		RecentLimit: recentLimit,
	})
	if err != nil {
		log.WithError(err).Warn("usagestats: summary query failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query_failed", "message": "failed to query usage statistics"})
		return
	}

	c.JSON(http.StatusOK, result)
}
