package management

// proxx: usage/spend and pricing management endpoints (ADR 0004/0005).
// Routes are mounted under /v8/management/proxx by internal/api/server_management_proxx_usage.go.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/pricing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/usagestats"
)

const proxxDayLayout = "2006-01-02"

// GetProxxUsageSummary returns totals and grouped rows for an inclusive UTC day range.
// Query: from, to (YYYY-MM-DD; default the last 7 days), group_by (model|provider|api_key|day).
func (h *Handler) GetProxxUsageSummary(c *gin.Context) {
	store := usagestats.Default()
	today := store.Today()
	to := strings.TrimSpace(c.Query("to"))
	from := strings.TrimSpace(c.Query("from"))
	if to == "" {
		to = today.Format(proxxDayLayout)
	}
	if from == "" {
		from = today.AddDate(0, 0, -6).Format(proxxDayLayout)
	}
	for _, day := range []string{from, to} {
		if _, err := time.Parse(proxxDayLayout, day); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from/to must be YYYY-MM-DD"})
			return
		}
	}
	if from > to {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must not be after to"})
		return
	}
	groupBy := strings.TrimSpace(c.DefaultQuery("group_by", "model"))
	valid := false
	for _, g := range usagestats.GroupBy {
		valid = valid || g == groupBy
	}
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_by must be one of model, provider, api_key, day"})
		return
	}
	c.JSON(http.StatusOK, store.Summary(from, to, groupBy))
}

// GetProxxUsageRecent returns the newest recent requests. Query: limit (default 50).
func (h *Handler) GetProxxUsageRecent(c *gin.Context) {
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		limit = n
	}
	c.JSON(http.StatusOK, gin.H{"items": usagestats.Default().Recent(limit)})
}

// DeleteProxxUsage resets all aggregated usage.
func (h *Handler) DeleteProxxUsage(c *gin.Context) {
	usagestats.Default().Reset()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetProxxPricing returns the catalog status and resolved prices for registry models.
func (h *Handler) GetProxxPricing(c *gin.Context) {
	status := pricing.Status()
	var updatedAt any
	if !status.UpdatedAt.IsZero() {
		updatedAt = status.UpdatedAt.Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, gin.H{
		"catalog_source":    status.Source,
		"updated_at":        updatedAt,
		"override_upstream": status.OverrideUpstream,
		"disabled":          status.Disabled,
		"models":            pricing.RegistryPrices(),
	})
}
