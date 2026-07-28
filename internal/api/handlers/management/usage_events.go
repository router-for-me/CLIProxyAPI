package management

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// pagedEventsResponse is the paginated envelope returned by
// /usage-stats/events. Total reflects the row count after the same filter so
// the caller can render a full pager without a second round-trip.
type pagedEventsResponse struct {
	Events   []store.UsageEventRow `json:"events"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	Total    int64                 `json:"total"`
}

// GetUsageEvents handles GET /v0/management/usage-stats/events.
//
// Query parameters:
//   - page      (default 1)
//   - page_size (default 25, max 200)
//   - include   (comma-separated extras; supported: "cost_breakdown")
//   - api_key_id, provider, model, from, to, limit (same filter semantics as /usage-stats)
//
// Returns the newest events first, each with the resolved non-secret
// key_alias in place of the sealed api_key_principal.
func (h *Handler) GetUsageEvents(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "25"))
	events, total, err := usage.SelectEvents(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	// Best-effort official_provider resolution; never blocks the listing.
	h.FillOfficialProvider(c.Request.Context(), events)
	if wantsCostBreakdown(c.Query("include")) {
		// Best-effort: a missing/errored pricing lookup should not block the
		// events listing. The dashboard falls back to cost_usd alone.
		if err := usage.FillCostBreakdown(c.Request.Context(), events); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
			return
		}
	}
	c.JSON(http.StatusOK, pagedEventsResponse{
		Events:   events,
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	})
}

// GetUsageEvent handles GET /v0/management/usage-stats/events/:id.
// Returns 404 when the primary key does not exist. The single-event payload
// always carries the full cost breakdown (the one extra GetPricing round-trip
// is negligible at row granularity, so no include= flag is needed here).
func (h *Handler) GetUsageEvent(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "id must be a positive integer"}})
		return
	}
	event, err := usage.GetEvent(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrUsageEventNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found", "message": "usage event not found"}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	// Best-effort official_provider resolution for the single-row detail modal.
	h.FillOfficialProvider(c.Request.Context(), []store.UsageEventRow{event})
	// Best-effort: a missing pricing row leaves the breakdown zero-valued but
	// present, so the dashboard can show "no price set" rather than hiding
	// the section. Errors here are logged but not surfaced to the API caller
	// — the rest of the event payload is still useful.
	if err := usage.FillCostBreakdown(c.Request.Context(), []store.UsageEventRow{event}); err != nil {
		// Fall through with the zero-value breakdown; do not fail the request.
		_ = err
	}
	c.JSON(http.StatusOK, gin.H{"event": event})
}

// wantsCostBreakdown reports whether the comma-separated include= query param
// lists "cost_breakdown". Trims whitespace and is case-insensitive.
func wantsCostBreakdown(include string) bool {
	for _, part := range strings.Split(include, ",") {
		if strings.EqualFold(strings.TrimSpace(part), "cost_breakdown") {
			return true
		}
	}
	return false
}

// GetUsageFilters handles GET /v0/management/usage-stats/filters.
//
// Returns the distinct api_keys / providers / models observed in the supplied
// filter window. The dashboard uses this to populate dropdown filter menus so
// operators never need to type free-text filter values (which is impossible
// against the sealed api_key_principal column).
func (h *Handler) GetUsageFilters(c *gin.Context) {
	_, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	q := parseUsageStatsQuery(c)
	filter := filterFromQuery(q)
	opts, err := usage.SelectFilterOptions(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, opts)
}
