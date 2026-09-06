package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// DeployRelayProxyPool handles POST /v0/management/proxy-pools/relay-deploy:
// one-shot deploy of a relay worker to Cloudflare/Vercel/Deno followed by a
// pool row creation. Implemented in Task 14; this handler currently reports
// the missing-platform error until the deploy flows land.
func (h *Handler) DeployRelayProxyPool(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{
		"type":    "not_implemented",
		"message": "relay deploy is not available yet",
	}})
}
