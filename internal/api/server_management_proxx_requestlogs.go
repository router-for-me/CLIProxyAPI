package api

import "github.com/gin-gonic/gin"

// registerProxxRequestLogRoutes mounts the request log viewer endpoints (ADR 0006).
// Request logging itself is toggled with PUT /v8/management/config/observability/logs/request-log.
func (s *Server) registerProxxRequestLogRoutes(proxx *gin.RouterGroup) {
	proxx.GET("/requests", s.mgmt.ListProxxRequestLogs)
	proxx.DELETE("/requests", s.mgmt.DeleteProxxRequestLogs)
	proxx.GET("/requests/stream", s.mgmt.StreamProxxRequestLogs)
	proxx.GET("/requests/:id", s.mgmt.GetProxxRequestLog)
	proxx.GET("/requests/:id/raw", s.mgmt.GetProxxRequestLogRaw)
	proxx.DELETE("/requests/:id", s.mgmt.DeleteProxxRequestLog)
}
