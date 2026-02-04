package management

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// PeerAuthMiddleware returns a middleware for peer-to-peer authentication.
// Both master and follower share the same secret-key value (typically a bcrypt hash),
// and this middleware does constant-time string comparison (not bcrypt verification).
// This differs from Middleware() which does bcrypt verification for human users.
func (h *Handler) PeerAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h == nil || h.cfg == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "peer authentication not configured"})
			return
		}
		expected := h.cfg.RemoteManagement.SecretKey
		if expected == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "peer authentication not configured"})
			return
		}

		// Accept Authorization: Bearer <secret> or X-Peer-Secret header
		var provided string
		if auth := c.GetHeader("Authorization"); auth != "" {
			parts := strings.SplitN(auth, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				provided = parts[1]
			}
		}
		if provided == "" {
			provided = c.GetHeader("X-Peer-Secret")
		}

		if provided == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing peer secret"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid peer secret"})
			return
		}
		c.Next()
	}
}

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
