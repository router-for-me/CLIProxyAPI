package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HandleCredentialQuery returns the current access_token for a given auth ID.
// This endpoint is used by follower nodes to fetch credentials from master.
func (h *Handler) HandleCredentialQuery(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server not initialized"})
		return
	}

	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id parameter is required"})
		return
	}

	h.authManager.RefreshIfNeeded(c.Request.Context(), id)

	accessToken := h.authManager.GetAccessToken(id)
	if accessToken == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found or no access_token"})
		return
	}

	response := gin.H{
		"id":           id,
		"access_token": accessToken,
	}
	if expiredAt, ok := h.authManager.GetExpirationTime(id); ok && !expiredAt.IsZero() {
		response["expired"] = expiredAt.Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, response)
}

// HandleAuthList returns all auth entries (without refresh_token).
// This endpoint is used by follower nodes for startup sync.
func (h *Handler) HandleAuthList(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server not initialized"})
		return
	}

	auths := h.authManager.GetAllAuthsForSync()
	c.JSON(http.StatusOK, gin.H{"auths": auths})
}
