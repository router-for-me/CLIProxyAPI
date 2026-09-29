package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// TelegramLegacyAuth retires the unsigned-flow widget endpoints. Existing
// Telegram bindings are used by the unified OAuth provider instead.
func TelegramLegacyAuth(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{
		"success": false,
		"code":    "TELEGRAM_LEGACY_AUTH_REMOVED",
		"message": "Telegram login has changed. Reload the page and start Telegram OAuth again.",
	})
}
