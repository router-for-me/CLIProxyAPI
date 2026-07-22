package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type sessionAffinityStatsResponse struct {
	Enabled        bool `json:"enabled"`
	ActiveSessions int  `json:"active_sessions"`
	ActiveBindings int  `json:"active_bindings"`
}

// GetSessionAffinityStats returns aggregate statistics for active
// session-affinity bindings without exposing session identifiers.
func (h *Handler) GetSessionAffinityStats(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	stats, enabled := manager.SessionAffinityStats()
	c.JSON(http.StatusOK, sessionAffinityStatsResponse{
		ActiveSessions: stats.ActiveSessions,
		Enabled:        enabled,
		ActiveBindings: stats.ActiveBindings,
	})
}
