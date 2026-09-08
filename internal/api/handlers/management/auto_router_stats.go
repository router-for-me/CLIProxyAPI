package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type autoRouterStatsQuery struct {
	RouterID string
	APIKeyID string
	From     time.Time
	To       time.Time
	Limit    int
	TopBy    string // "tier" | "model"
}

func parseAutoRouterStatsQuery(c *gin.Context) autoRouterStatsQuery {
	q := autoRouterStatsQuery{
		RouterID: strings.TrimSpace(c.Query("router_id")),
		APIKeyID: strings.TrimSpace(c.Query("api_key_id")),
		TopBy:    c.DefaultQuery("top", "tier"),
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

// maxAutoRouterStatsWindow bounds the time range of the aggregation endpoints:
// jsonb aggregate scans over an unbounded window are too expensive.
const maxAutoRouterStatsWindow = 90 * 24 * time.Hour

// defaultAutoRouterStatsWindow is applied when neither from nor to is given.
const defaultAutoRouterStatsWindow = 7 * 24 * time.Hour

// applyStatsWindow fills in the default window when both bounds are zero and
// reports whether the resulting window is within the allowed maximum.
func applyStatsWindow(q *autoRouterStatsQuery) bool {
	if q.From.IsZero() && q.To.IsZero() {
		q.To = time.Now().UTC()
		q.From = q.To.Add(-defaultAutoRouterStatsWindow)
	}
	window := q.To.Sub(q.From)
	return window >= 0 && window <= maxAutoRouterStatsWindow
}

// GetAutoRouterStats handles GET /v0/management/auto-routers/stats. Returns
// per-tier request stats (top=tier, default), per-target-model cost stats
// (top=model), tier performance metrics (top=performance), or the decision
// distribution rollup (top=decision-stats) for a router, scoped by api_key_id
// and time range.
func (h *Handler) GetAutoRouterStats(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseAutoRouterStatsQuery(c)
	if q.RouterID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "router_id is required"}})
		return
	}
	if !applyStatsWindow(&q) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "time range exceeds 90 days"}})
		return
	}
	filter := store.UsageFilter{
		RouterID: q.RouterID,
		APIKeyID: q.APIKeyID,
		From:     q.From,
		To:       q.To,
		Limit:    q.Limit,
	}
	aggCtx := c.Request.Context()
	resp := gin.H{}
	switch q.TopBy {
	case "model":
		models, err := usage.SelectAutoRouterModelStats(aggCtx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		resp["models"] = models
	case "performance":
		perf, err := usage.SelectAutoRouterTierPerformance(aggCtx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		resp["performance"] = perf
	case "decision-stats":
		stats, err := usage.SelectAutoRouterDecisionStats(aggCtx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		resp["decision_stats"] = stats
	default:
		tiers, err := usage.SelectAutoRouterTierStats(aggCtx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		resp["tiers"] = tiers
	}
	c.JSON(http.StatusOK, resp)
}
