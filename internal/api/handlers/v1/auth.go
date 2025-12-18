// Package v1 provides versioned API handlers for KorProxy management.
package v1

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthRequired returns middleware that enforces authentication for write operations.
// It accepts either:
//   - Authorization: Bearer <token>
//   - X-Management-Key: <token>
func AuthRequired(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if secret == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "management key not configured"})
			return
		}

		provided := extractToken(c)
		if provided == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authentication"})
			return
		}

		if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authentication"})
			return
		}

		c.Next()
	}
}

// AuthOptional returns middleware that allows but doesn't require authentication.
// It sets authentication context if valid credentials are provided.
func AuthOptional(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if secret == "" {
			c.Next()
			return
		}

		provided := extractToken(c)
		if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 1 {
			c.Set("authenticated", true)
		}

		c.Next()
	}
}

// extractToken extracts authentication token from request headers.
// Checks Authorization: Bearer <token> first, then X-Management-Key.
func extractToken(c *gin.Context) string {
	if auth := c.GetHeader("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return strings.TrimSpace(parts[1])
		}
		return ""
	}

	return strings.TrimSpace(c.GetHeader("X-Management-Key"))
}
