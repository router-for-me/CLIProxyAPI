package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/observability"
)

// GetObservabilitySnapshot returns process-lifetime normalized request totals
// and a bounded list of recent request events. Cost values are estimates.
func (h *Handler) GetObservabilitySnapshot(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	after, err := parseObservabilityUint(c.Query("after"), "after")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit := uint64(200)
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		limit, err = parseObservabilityUint(rawLimit, "limit")
		if err != nil || limit == 0 {
			if err == nil {
				err = &observabilityQueryError{field: "limit", positive: true}
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if limit > 500 {
		limit = 500
	}

	tracker := observability.DefaultTracker()
	expectedBootID := strings.TrimSpace(c.Query("boot_id"))
	cursorReset := expectedBootID != "" && expectedBootID != tracker.BootID()
	if cursorReset {
		after = 0
	}
	snapshot := tracker.SnapshotAfter(after, int(limit))
	snapshot.CursorReset = cursorReset
	c.JSON(http.StatusOK, snapshot)
}

type observabilityQueryError struct {
	field    string
	positive bool
}

func (e *observabilityQueryError) Error() string {
	if e.positive {
		return e.field + " must be a positive integer"
	}
	return e.field + " must be a non-negative integer"
}

func parseObservabilityUint(raw, field string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, &observabilityQueryError{field: field}
	}
	return value, nil
}
