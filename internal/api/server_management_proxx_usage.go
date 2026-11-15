package api

import "github.com/gin-gonic/gin"

// registerProxxUsageRoutes mounts the proxx usage/spend and pricing endpoints (ADR 0004/0005).
// Never use /observability/usage/queue for these views: it pops records.
func (s *Server) registerProxxUsageRoutes(proxx *gin.RouterGroup) {
	proxx.GET("/usage/summary", s.mgmt.GetProxxUsageSummary)
	proxx.GET("/usage/recent", s.mgmt.GetProxxUsageRecent)
	proxx.DELETE("/usage", s.mgmt.DeleteProxxUsage)
	proxx.GET("/pricing", s.mgmt.GetProxxPricing)
}
