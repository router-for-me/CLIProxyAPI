package user

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ============================================================
// JWT 鉴权中间件 + 管理员权限中间件
// ============================================================

// JWTMiddleware JWT 鉴权中间件
func JWTMiddleware(jwtMgr *JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 Authorization 头"})
			c.Abort()
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization 格式错误，需要 Bearer token"})
			c.Abort()
			return
		}

		claims, err := jwtMgr.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 无效: " + err.Error()})
			c.Abort()
			return
		}

		if claims.Type != "access" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "需要 Access Token"})
			c.Abort()
			return
		}

		// 设置用户信息到 Gin Context
		c.Set("userID", claims.UserID)
		c.Set("userUUID", claims.UUID)
		c.Set("userRole", claims.Role)
		c.Next()
	}
}

// AdminMiddleware 管理员权限中间件（需在 JWTMiddleware 之后使用）
func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := c.GetString("userRole")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			c.Abort()
			return
		}
		c.Next()
	}
}
