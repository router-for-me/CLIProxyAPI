package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// ListRoutingModels lists every registered model with the providers that serve it.
// The response holds provider names and client counts only, never client identifiers.
func (h *Handler) ListRoutingModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"models": registry.GetGlobalRegistry().GetModelProviderCounts(),
	})
}
