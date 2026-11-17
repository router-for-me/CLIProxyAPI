package api

import "github.com/gin-gonic/gin"

// registerProxxUpdateRoutes mounts the self-update endpoints (ADR 0007). Config changes go through
// PUT /v8/management/config/proxx/update/<key>.
func (s *Server) registerProxxUpdateRoutes(proxx *gin.RouterGroup) {
	proxx.GET("/update", s.mgmt.GetProxxUpdate)
	proxx.POST("/update/check", s.mgmt.PostProxxUpdateCheck)
	proxx.POST("/update/apply", s.mgmt.PostProxxUpdateApply)
	proxx.POST("/update/cancel", s.mgmt.PostProxxUpdateCancel)
}
