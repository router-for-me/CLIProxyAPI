package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handler) GetOAuthProviders(c *gin.Context) {
	if h == nil || h.sdkAuthManager == nil {
		c.JSON(http.StatusOK, gin.H{"providers": []struct{}{}})
		return
	}
	providers := h.sdkAuthManager.ListProviders()
	c.JSON(http.StatusOK, gin.H{"providers": providers})
}
