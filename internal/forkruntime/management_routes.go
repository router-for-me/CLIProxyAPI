package forkruntime

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
)

// RegisterManagementRoutes registers this fork's conflict-prone management extensions only:
// persisted usage statistics and auth refresh queue.
// Callers should pass the authenticated /v0/management group.
func RegisterManagementRoutes(group gin.IRoutes, handler *management.Handler) {
	if group == nil || handler == nil {
		return
	}

	group.GET("/usage", handler.GetUsageStatistics)
	group.DELETE("/usage", handler.DeleteUsageRecords)
	group.GET("/auth-refresh-queue", handler.GetAuthRefreshQueue)
}
