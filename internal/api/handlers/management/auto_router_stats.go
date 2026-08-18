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

// GetAutoRouterStats handles GET /v0/management/auto-routers/stats. Returns
// per-tier request stats (top=tier, default) or per-target-model cost stats
// (top=model) for a router, scoped by api_key_id and time range.
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
	filter := store.UsageFilter{
		RouterID: q.RouterID,
		APIKeyID: q.APIKeyID,
		From:     q.From,
		To:       q.To,
		Limit:    q.Limit,
	}
	aggCtx := c.Request.Context()
	resp := gin.H{}
	if q.TopBy == "model" {
		models, err := usage.SelectAutoRouterModelStats(aggCtx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		resp["models"] = models
	} else {
		tiers, err := usage.SelectAutoRouterTierStats(aggCtx, filter)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
		resp["tiers"] = tiers
	}
	c.JSON(http.StatusOK, resp)
}
