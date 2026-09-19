package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cachestats"
)

// GetModelCacheStats returns per-model prompt cache hit statistics aggregated
// in memory from the usage pipeline.
func (h *Handler) GetModelCacheStats(c *gin.Context) {
	windowHours := 24
	if raw := strings.TrimSpace(c.Query("hours")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "hours must be a positive integer"})
			return
		}
		windowHours = parsed
	}
	includeHourly := strings.EqualFold(strings.TrimSpace(c.Query("hourly")), "true")
	c.JSON(http.StatusOK, cachestats.GetSnapshot(windowHours, includeHourly))
}
