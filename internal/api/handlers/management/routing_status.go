package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func (h *Handler) GetRoutingStatus(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	model := strings.TrimSpace(c.Query("model"))
	if provider == "" || len(provider) > 64 || len(model) > 256 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required; provider/model too long"})
		return
	}
	pool := strings.ToLower(strings.TrimSpace(c.Query("pool")))
	if pool != "" {
		known := false
		for _, p := range coreauth.RoutingPools(provider) {
			known = known || p.Key == pool
		}
		if !known || model != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "pool must be one of the provider's pools and excludes model"})
			return
		}
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing manager unavailable"})
		return
	}
	ctx := c.Request.Context()
	switch c.DefaultQuery("transport", "http") {
	case "http":
	case "websocket":
		ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid transport"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.authManager.RoutingStatusForPool(ctx, provider, model, pool, time.Now()))
}
