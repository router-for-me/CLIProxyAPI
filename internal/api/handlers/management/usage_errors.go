package management

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// pagedErrorsResponse is the paginated envelope returned by
// /usage-stats/errors. Total reflects the row count after the same filter so
// the caller can render a full pager without a second round-trip.
type pagedErrorsResponse struct {
	Errors   []store.UsageErrorRow `json:"errors"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	Total    int64                 `json:"total"`
}

// GetUsageErrors handles GET /v0/management/usage-stats/errors.
//
// Query parameters:
//   - page      (default 1)
//   - page_size (default 25, max 200)
//   - include   (comma-separated extras; supported: "cost_breakdown")
//   - api_key_id, provider, model, from, to, limit (same filter semantics as /usage-stats)
//
// Returns the newest failed attempts first, each with the resolved non-secret
// key_alias in place of the sealed api_key_principal.
func (h *Handler) GetUsageErrors(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	rows, total, err := usage.SelectErrors(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	// Best-effort official_provider resolution; never blocks the listing.
	h.FillOfficialProviderErrors(c.Request.Context(), rows)
	if wantsCostBreakdown(c.Query("include")) {
		// Best-effort: a missing/errored pricing lookup should not block the
		// errors listing. Mirrors the events listing behavior.
		if err := usage.FillCostBreakdownErrors(c.Request.Context(), rows); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
	}
	c.JSON(http.StatusOK, pagedErrorsResponse{
		Errors:   rows,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	})
}

// GetUsageError handles GET /v0/management/usage-stats/errors/:id.
// Returns 404 when the primary key does not exist. The single-error payload
// always carries the full cost breakdown (the one extra GetPricing round-trip
// is negligible at row granularity, so no include= flag is needed here).
func (h *Handler) GetUsageError(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "id must be a positive integer"}})
		return
	}
	errRow, err := usage.GetError(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrUsageErrorNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "usage error not found"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Best-effort official_provider resolution for the single-row detail modal.
	h.FillOfficialProviderErrors(c.Request.Context(), []store.UsageErrorRow{errRow})
	// Best-effort: a missing pricing row leaves the breakdown zero-valued but
	// present, so the dashboard can show "no price set" rather than hiding the
	// section. Mirrors GetUsageEvent.
	if err := usage.FillCostBreakdownErrors(c.Request.Context(), []store.UsageErrorRow{errRow}); err != nil {
		_ = err
	}
	c.JSON(http.StatusOK, gin.H{"error_event": errRow})
}

// GetInternalUserErrors handles GET /v0/management/internal-users/:id/errors.
//
// Paged raw failed-attempt rows for the user, newest first. The principal
// column is resolved to the non-secret key_alias via the api_keys LEFT JOIN
// (per-store-level SelectErrors). Mirrors GetInternalUserEvents.
func (h *Handler) GetInternalUserErrors(c *gin.Context) {
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
	rows, total, err := usage.SelectErrors(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	if c.Query("include") == "cost_breakdown" {
		if err := usage.FillCostBreakdownErrors(c.Request.Context(), rows); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"errors":      rows,
		"page":        page,
		"page_size":   pageSize,
		"total":       total,
		"total_pages": totalPages(total, pageSize),
		"user_id":     userID,
	})
}
