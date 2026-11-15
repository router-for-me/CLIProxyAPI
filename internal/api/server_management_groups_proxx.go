package api

import "github.com/gin-gonic/gin"

// registerProxxGroupRoutes mounts the model-groups management routes under /v8/management/proxx.
func (s *Server) registerProxxGroupRoutes(proxx *gin.RouterGroup) {
	proxx.GET("/groups", s.mgmt.GetProxxModelGroups)
}
