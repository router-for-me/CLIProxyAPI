package management

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
)

type CircuitBreakerDeletionActionHandler interface {
	DeleteCircuitBreakerDeletion(ctx context.Context, id string, actionBy string) (mongostate.CircuitBreakerDeletionItem, error)
	DismissCircuitBreakerDeletion(ctx context.Context, id string, actionBy string) (mongostate.CircuitBreakerDeletionItem, error)
}

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
		Status:   strings.ToLower(strings.TrimSpace(c.Query("status"))),
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

func (h *Handler) DeleteCircuitBreakerDeletion(c *gin.Context) {
	if h == nil || h.circuitBreakerDeletionActionHandler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "circuit breaker deletion action handler unavailable"})
		return
	}

	item, err := h.circuitBreakerDeletionActionHandler.DeleteCircuitBreakerDeletion(c.Request.Context(), c.Param("id"), "management_api")
	h.respondCircuitBreakerDeletionAction(c, item, err)
}

func (h *Handler) DismissCircuitBreakerDeletion(c *gin.Context) {
	if h == nil || h.circuitBreakerDeletionActionHandler == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "circuit breaker deletion action handler unavailable"})
		return
	}

	item, err := h.circuitBreakerDeletionActionHandler.DismissCircuitBreakerDeletion(c.Request.Context(), c.Param("id"), "management_api")
	h.respondCircuitBreakerDeletionAction(c, item, err)
}

func (h *Handler) respondCircuitBreakerDeletionAction(c *gin.Context, item mongostate.CircuitBreakerDeletionItem, err error) {
	switch {
	case err == nil:
		c.JSON(http.StatusOK, item)
	case errors.Is(err, mongostate.ErrCircuitBreakerDeletionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, mongostate.ErrCircuitBreakerDeletionConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "item": item})
	default:
		if item.ID == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "item": item})
	}
}
