package management

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// GetInternalUserTotals handles GET /v0/management/internal-users/:id/totals.
//
// Returns the rolled-up KPI card values for the supplied user and time window:
// total request count, failed count, token sums, and cost USD. Cheap query (no
// GROUP BY), suitable for the dashboard's top-of-page summary tiles.
//
// Query parameters shared with all per-user stats endpoints:
//
//	provider, model, from (RFC3339), to (RFC3339), limit
func (h *Handler) GetInternalUserTotals(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	userID := c.Param("id")
	q := parseUsageStatsQuery(c)
	q.UserID = userID
	filter := filterFromQuery(q)
	totals, err := usage.SelectTotals(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// failed_count is sourced from usage_errors (failed attempts no longer
	// live in usage_events). request_count counts only successes, so the
	// failure rate is computed over total attempts (successes + failures) to
	// stay meaningful under credential failover.
	failedCount, err := usage.SelectErrorCount(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	totals.FailedCount = failedCount
	totalAttempts := totals.RequestCount + totals.FailedCount
	var failureRate float64
	if totalAttempts > 0 {
		failureRate = float64(totals.FailedCount) / float64(totalAttempts) * 100
	}
	c.JSON(http.StatusOK, gin.H{
		"totals":         totals,
		"failure_rate":   failureRate,
		"success_count":  totals.RequestCount,
		"total_attempts": totalAttempts,
		"user_id":        userID,
	})
}

// GetInternalUserTimeSeries handles GET /v0/management/internal-users/:id/timeseries.
//
//   - interval: "minute" | "hour" (default) | "day"
func (h *Handler) GetInternalUserTimeSeries(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	userID := c.Param("id")
	q := parseUsageStatsQuery(c)
	q.UserID = userID
	filter := filterFromQuery(q)
	interval := c.DefaultQuery("interval", "hour")
	ts, err := usage.SelectTimeSeries(c.Request.Context(), filter, interval)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"points": ts, "interval": interval, "user_id": userID})
}

// GetInternalUserTop handles GET /v0/management/internal-users/:id/top.
//
//   - dimension: "model" | "provider" | "api_key_id"
//     ("user_id" / "api_key_principal" are not meaningful when scoped to a user)
//   - metric:    "request_count" (default) | "total_tokens" | "cost_usd"
//   - limit:     default 10, max 500
func (h *Handler) GetInternalUserTop(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	userID := c.Param("id")
	dimension := c.DefaultQuery("dimension", "model")
	metric := c.DefaultQuery("metric", "request_count")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if limit <= 0 {
		limit = 10
	}
	q := parseUsageStatsQuery(c)
	q.UserID = userID
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
		"user_id":   userID,
	})
}

// GetInternalUserEvents handles GET /v0/management/internal-users/:id/events.
//
// Paged raw usage events for the user, newest first. The principal column is
// resolved to the non-secret key_alias via the api_keys LEFT JOIN
// (per-store-level SelectEvents).
func (h *Handler) GetInternalUserEvents(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	userID := c.Param("id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	q := parseUsageStatsQuery(c)
	q.UserID = userID
	filter := filterFromQuery(q)
	rows, total, err := usage.SelectEvents(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Best-effort official_provider resolution; never blocks the listing.
	h.FillOfficialProvider(c.Request.Context(), rows)
	if c.Query("include") == "cost_breakdown" {
		if err := usage.FillCostBreakdown(c.Request.Context(), rows); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"events":      rows,
		"page":        page,
		"page_size":   pageSize,
		"total":       total,
		"total_pages": totalPages(total, pageSize),
		"user_id":     userID,
	})
}

// GetInternalUserWindows handles GET /v0/management/internal-users/:id/windows.
//
// Returns the per-user budget windows (hourly/weekly/monthly) used by the
// dashboard's "Budget vs usage" card.
func (h *Handler) GetInternalUserWindows(c *gin.Context) {
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	users := h.pgUsers
	userID := c.Param("id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 {
		limit = 50
	}
	windows, err := users.ListUserWindows(c.Request.Context(), userID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"windows": windows, "user_id": userID})
}

// internalUserLeaderboardEntry pairs the user object with benchmark metrics
// for the leaderboard view.
type internalUserLeaderboardEntry struct {
	UserID    string   `json:"user_id"`
	UserAlias string   `json:"user_alias"`
	UserEmail string   `json:"user_email,omitempty"`
	UserRole  string   `json:"user_role"`
	Spend     float64  `json:"spend"`
	MaxBudget *float64 `json:"max_budget,omitempty"`
	RPMLimit  *int64   `json:"rpm_limit,omitempty"`
	TPMLimit  *int64   `json:"tpm_limit,omitempty"`
	KeyCount  int64    `json:"key_count"`
}

// GetInternalUsersLeaderboard handles GET /v0/management/internal-users/leaderboard.
//
// Returns the top-N internal users by spend, mirroring LiteLLM's
// /spend/users spend leaderboard. Time-window filtering (from/to) is applied
// server-side via SelectTop so per-window spend leaderboard is also supported.
//
//   - limit: default 10, max 200
//   - metric (for time-windowed ranking): default "cost_usd" | "total_tokens" | "request_count"
//     (ignored when from/to are not supplied — falls back to running spend)
func (h *Handler) GetInternalUsersLeaderboard(c *gin.Context) {
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if limit <= 0 {
		limit = 10
	}
	if limit > 200 {
		limit = 200
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	// Time-windowed leaderboard: use SelectTop(dimension=user_id) scoped by
	// the from/to window. Falls through to running-spend ordering when neither
	// bound is supplied.
	from := c.Query("from")
	to := c.Query("to")
	if from != "" || to != "" {
		_, usage, _, _, ok := h.requirePG(c)
		if !ok {
			return
		}
		var fromT, toT time.Time
		if from != "" {
			if t, err := time.Parse(time.RFC3339, from); err == nil {
				fromT = t
			}
		}
		if to != "" {
			if t, err := time.Parse(time.RFC3339, to); err == nil {
				toT = t
			}
		}
		metric := c.DefaultQuery("metric", "cost_usd")
		entries, err := usage.SelectTop(c.Request.Context(), store.UsageFilter{
			From: fromT,
			To:   toT,
		}, "user_id", metric, limit)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
			return
		}
		out := make([]internalUserLeaderboardEntry, 0, len(entries))
		for _, e := range entries {
			// e.Key carries the resolved alias when joinExtra matched; we
			// try to enrich with the full user object below.
			entry := internalUserLeaderboardEntry{
				UserAlias: e.Key,
				Spend:     e.CostUSD,
			}
			// Try fetching the full user by ID (when SelectTop projected an
			// id rather than alias — principal's first COALESCE branch).
			if u, err := users.Get(c.Request.Context(), e.Key); err == nil {
				entry.UserID = u.ID
				entry.UserAlias = u.UserAlias
				entry.UserEmail = u.UserEmail
				entry.UserRole = u.UserRole
				entry.MaxBudget = u.MaxBudget
				entry.RPMLimit = u.RPMLimit
				entry.TPMLimit = u.TPMLimit
				entry.Spend = u.Spend
				entry.KeyCount = u.KeyCount
			} else {
				entry.UserID = e.Key
			}
			out = append(out, entry)
		}
		c.JSON(http.StatusOK, gin.H{"entries": out, "metric": metric, "limit": limit})
		return
	}
	list, _, err := users.ListWithSpend(c.Request.Context(), store.ListFilter{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    "spend",
		SortOrder: "desc",
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	capped := list
	if len(capped) > limit {
		capped = capped[:limit]
	}
	out := make([]internalUserLeaderboardEntry, 0, len(capped))
	for _, u := range capped {
		out = append(out, internalUserLeaderboardEntry{
			UserID:    u.ID,
			UserAlias: u.UserAlias,
			UserEmail: u.UserEmail,
			UserRole:  u.UserRole,
			Spend:     u.Spend,
			MaxBudget: u.MaxBudget,
			RPMLimit:  u.RPMLimit,
			TPMLimit:  u.TPMLimit,
			KeyCount:  u.KeyCount,
		})
	}
	c.JSON(http.StatusOK, gin.H{"entries": out, "limit": limit, "page": page})
}

// GetInternalUserModelSpend handles GET /v0/management/internal-users/:id/model-spend.
//
// Returns per-model aggregations (cost / tokens / request count) computed
// on-the-fly from usage_events. Scoped by the same from/to/interval query
// parameters used by the other per-user stats endpoints.
func (h *Handler) GetInternalUserModelSpend(c *gin.Context) {
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	users := h.pgUsers
	userID := c.Param("id")
	var from, to time.Time
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	entries, err := users.GetModelSpend(c.Request.Context(), userID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries, "user_id": userID})
}

// GetInternalUserModels handles GET /v0/management/internal-users/:id/models.
//
// Returns the configured grant list (allowed_models), per-model cap JSON
// (optional, stored in metadata.model_max_budgets when set), and the
// realized per-model spend computed on-the-fly. The dashboard uses this to
// render the "Per-model spend" card with a budget-vs-usage bar.
func (h *Handler) GetInternalUserModels(c *gin.Context) {
	if _, ok := h.requireUsers(c); !ok {
		return
	}
	users := h.pgUsers
	userID := c.Param("id")
	u, err := users.Get(c.Request.Context(), userID)
	if err != nil {
		h.translateUserError(c, err)
		return
	}
	modelMaxBudgets := map[string]float64{}
	if u.Metadata != nil {
		if v, ok := u.Metadata["model_max_budgets"]; ok {
			if m, ok := v.(map[string]any); ok {
				for k, val := range m {
					if f, ok := toFloat(val); ok {
						modelMaxBudgets[k] = f
					}
				}
			}
		}
	}
	var from, to time.Time
	entries, _ := users.GetModelSpend(c.Request.Context(), userID, from, to)
	c.JSON(http.StatusOK, gin.H{
		"user_id":           userID,
		"allowed_models":    u.Models,
		"model_max_budgets": modelMaxBudgets,
		"model_spend":       entries,
	})
}

// toFloat coerces a JSON-decoded numeric value into a float64, returns
// (0, false) when the value is not a number.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
