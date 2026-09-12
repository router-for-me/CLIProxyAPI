package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// GetClaudeClientVersions reports the configured Claude Code User-Agent baseline
// alongside the CLI versions actually observed per credential.
//
// The baseline in claude-header-defaults.user-agent is a floor rather than a
// lock, so a client that auto-updated past it is still passed through. That is
// exactly what makes a stale pin easy to miss: cloaked requests keep presenting
// the configured constant. This read-only endpoint makes the drift inspectable.
func (h *Handler) GetClaudeClientVersions(c *gin.Context) {
	c.JSON(http.StatusOK, helps.ClaudeClientVersionReport(h.cfg))
}
