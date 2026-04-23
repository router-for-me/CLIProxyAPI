package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
)

func parseRFC3339QueryTime(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

func parsePositiveIntQuery(raw string, fallback int) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// GetCircuitBreakerDeletions returns paged MongoDB audit records for auto-deleted models.
func (h *Handler) GetCircuitBreakerDeletions(c *gin.Context) {
	store := mongostate.GetGlobalCircuitBreakerDeletionStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "circuit breaker deletion store unavailable"})
		return
	}

	start, err := parseRFC3339QueryTime(c.Query("start"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start time, expected RFC3339"})
		return
	}
	end, err := parseRFC3339QueryTime(c.Query("end"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end time, expected RFC3339"})
		return
	}

	query := mongostate.CircuitBreakerDeletionQuery{
		Provider: strings.ToLower(strings.TrimSpace(c.Query("provider"))),
		AuthID:   strings.TrimSpace(c.Query("auth_id")),
		Model:    strings.TrimSpace(c.Query("model")),
		Start:    start,
		End:      end,
		Page:     parsePositiveIntQuery(c.Query("page"), 1),
		PageSize: parsePositiveIntQuery(c.Query("page_size"), 20),
	}

	result, err := store.Query(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
