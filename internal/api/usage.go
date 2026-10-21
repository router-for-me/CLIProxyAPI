package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// handleUserUsage serves upstream quota limits for proxy API-key clients.
// The endpoint is absent unless explicitly enabled with a provider allow-list,
// and it never accepts management credentials in place of a proxy key.
func (s *Server) handleUserUsage(c *gin.Context) {
	if s == nil || s.usageAPI == nil || !s.usageAPI.Enabled() {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	principal, exists := c.Get("userApiKey")
	apiKey, ok := principal.(string)
	if !exists || !ok || strings.TrimSpace(apiKey) == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{
			"message": "A valid proxy API key is required.",
			"type":    "authentication_error",
			"code":    "invalid_api_key",
		}})
		return
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.usageAPI.Snapshot(c.Request.Context()))
}
