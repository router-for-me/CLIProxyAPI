package management

import (
	"github.com/gin-gonic/gin"
)

// requireLiteLLMRuntime returns false (after writing a 503 response) when the
// runtime PG stores backing the compat /litellm routes are not wired. Mirrors
// requirePG / requireUsers. Guard before every compat handler.
func (h *Handler) requireLiteLLMRuntime(c *gin.Context) bool {
	if h == nil {
		h.pgNotConfigured(c)
		return false
	}
	h.mu.Lock()
	users, keys, usage := h.pgUsers, h.pgAPIKeys, h.pgUsage
	h.mu.Unlock()
	if users == nil || keys == nil || usage == nil {
		h.pgNotConfigured(c)
		return false
	}
	return true
}
