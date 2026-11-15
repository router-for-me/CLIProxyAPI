package api

import "github.com/gin-gonic/gin"

// registerProxxManagementRoutes mounts fork-only management routes under /v8/management/proxx.
// It is the single hook from the upstream route table (server_management_v8.go); add new
// proxx endpoints here so upstream merges never conflict on the route table.
func (s *Server) registerProxxManagementRoutes(v8 *gin.RouterGroup) {
	proxx := v8.Group("/proxx")
	s.registerProxxUsageRoutes(proxx) // usage + pricing (server_management_proxx_usage.go)
}
