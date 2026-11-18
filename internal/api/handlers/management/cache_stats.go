package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cachestats"
)

// GetCacheStats reports prompt-cache reads and writes per credential auth index,
// for the last cachestats.Window and since the process started.
func (h *Handler) GetCacheStats(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"window_seconds": int(cachestats.Window.Seconds()),
		"credentials":    cachestats.Snapshot(),
	})
}
