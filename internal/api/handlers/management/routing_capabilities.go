package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetRoutingCapabilities reports implemented policies and their current scheduler
// compatibility. A saved configuration field alone does not establish support.
func (h *Handler) GetRoutingCapabilities(c *gin.Context) {
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"provider_affinity_overrides": true,
		"credential_priority_cas":     true,
		"local_builtin_selector":      manager != nil && manager.LocalBuiltinSelector(),
	})
}
