package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	// error_class / error_fingerprint are errors-only columns. They are parsed
	// here directly rather than through parseUsageStatsQuery / filterFromQuery so
	// that events-path handlers (which share those helpers) never see them.
	if v := strings.TrimSpace(c.Query("error_class")); v != "" {
		filter.ErrorClass = v
	}
	if v := strings.TrimSpace(c.Query("error_fingerprint")); v != "" {
		filter.ErrorFingerprint = v
	}
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
	//
	// The fill helpers mutate rows[i] in place via index assignment, which only
	// lands on their slice element. Pass a local slice that wraps the local
	// `errRow` variable and reassign it back, otherwise the mutations attach to
	// the implicit copy made when the slice literal is constructed and `errRow`
	// (the value actually returned below) keeps nil CostBreakdown/AppliedPricing.
	// Without this reassign the dashboard's "Cost breakdown" branch never
	// renders on the error detail modal. Mirrors GetUsageEvent.
	rows := []store.UsageErrorRow{errRow}
	h.FillOfficialProviderErrors(c.Request.Context(), rows)
	// Best-effort: a missing pricing row leaves the breakdown zero-valued but
	// present, so the dashboard can show "no price set" rather than hiding the
	// section. Mirrors GetUsageEvent.
	if err := usage.FillCostBreakdownErrors(c.Request.Context(), rows); err != nil {
		_ = err
	}
	errRow = rows[0]
	c.JSON(http.StatusOK, gin.H{"error_event": errRow})
}

// GetErrorSummary handles GET /v0/management/usage-stats/errors/summary.
//
// Returns a multi-rollup summary for the dashboard's at-a-glance failure cards:
// total count, distributions by class/status/provider, and top 10 models.
// Accepts the same filter parameters as /usage-stats/errors.
func (h *Handler) GetErrorSummary(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	// error_class / error_fingerprint are errors-only columns.
	if v := strings.TrimSpace(c.Query("error_class")); v != "" {
		filter.ErrorClass = v
	}
	if v := strings.TrimSpace(c.Query("error_fingerprint")); v != "" {
		filter.ErrorFingerprint = v
	}

	summary, err := usage.SelectErrorSummary(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GetErrorGroups handles GET /v0/management/usage-stats/errors/groups.
//
// Query parameters:
//   - group_by: "class" (default) | "fingerprint" | "provider" | "model" | "status"
//   - limit:    default 20, max 200
//   - plus the usual filter set (api_key_id, provider, model, from, to, error_class, error_fingerprint)
//
// Returns groups sorted by count descending, each with sample message and metadata.
func (h *Handler) GetErrorGroups(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	if v := strings.TrimSpace(c.Query("error_class")); v != "" {
		filter.ErrorClass = v
	}
	if v := strings.TrimSpace(c.Query("error_fingerprint")); v != "" {
		filter.ErrorFingerprint = v
	}

	// Validate group_by in the handler before calling the store so an invalid
	// value yields 400 (the store returns a plain error for unsupported group_by
	// with no sentinel type — validating here is cleaner than string-matching).
	groupBy := strings.TrimSpace(c.DefaultQuery("group_by", "class"))
	groupBy = strings.ToLower(groupBy)
	switch groupBy {
	case "class", "fingerprint", "provider", "model", "status":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "group_by must be one of: class|fingerprint|provider|model|status"}})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	groups, err := usage.SelectErrorGroups(c.Request.Context(), filter, groupBy, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"group_by": groupBy,
		"groups":   groups,
	})
}

// GetErrorTimeline handles GET /v0/management/usage-stats/errors/timeline.
//
// Query parameters:
//   - interval: "minute" | "hour" (default) | "day"
//   - plus the usual filter set (api_key_id, provider, model, from, to, error_class, error_fingerprint)
//
// Returns one series per error class, each with (bucket, count) points sorted
// ascending by bucket. Zero-bucket classes are omitted.
func (h *Handler) GetErrorTimeline(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	if v := strings.TrimSpace(c.Query("error_class")); v != "" {
		filter.ErrorClass = v
	}
	if v := strings.TrimSpace(c.Query("error_fingerprint")); v != "" {
		filter.ErrorFingerprint = v
	}

	// Validate interval in the handler before calling the store. intervalExpr
	// returns a plain error for unsupported values with no sentinel type, so
	// handler-side validation gives us a clear 400 vs 500 boundary.
	interval := strings.TrimSpace(c.DefaultQuery("interval", "hour"))
	interval = strings.ToLower(interval)
	switch interval {
	case "minute", "hour", "day":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "interval must be one of: minute|hour|day"}})
		return
	}

	series, err := usage.SelectErrorTimeline(c.Request.Context(), filter, interval)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"interval": interval,
		"series":   series,
	})
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
	// error_class / error_fingerprint are errors-only columns.
	if v := strings.TrimSpace(c.Query("error_class")); v != "" {
		filter.ErrorClass = v
	}
	if v := strings.TrimSpace(c.Query("error_fingerprint")); v != "" {
		filter.ErrorFingerprint = v
	}
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
