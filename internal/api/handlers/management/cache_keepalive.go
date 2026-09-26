package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/keepalive"
)

// GetCacheKeepalive reports the state of the Claude Code prompt-cache keepalive
// scheduler: every tracked session with its probe history, plus the process-wide
// counters.
//
// It is read-only and takes no parameters. Sessions that have stopped probing
// stay listed with the reason, so an operator can tell "nothing to do" apart
// from "silently broken". Request bodies are dropped the moment a session
// retires and are never exposed here.
//
//	GET /v0/management/cache-keepalive
func (h *Handler) GetCacheKeepalive(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	c.JSON(http.StatusOK, keepalive.Default().Snapshot())
}
