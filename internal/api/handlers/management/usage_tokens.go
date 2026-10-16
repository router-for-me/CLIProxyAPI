package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usageaggregate"
)

// GetTokenUsage returns the in-memory token usage distribution aggregated from
// proxy runtime usage records. The breakdown separates input tokens (uncached,
// cache read, cache write), output tokens (non-reasoning and reasoning/thinking),
// and unclassified tokens, both overall and per credential and provider.
func (h *Handler) GetTokenUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	c.JSON(http.StatusOK, usageaggregate.Snapshot())
}
