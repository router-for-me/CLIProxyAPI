package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed hub_dashboard.html
var hubDashboardHTML []byte

// serveHubDashboard serves the hub dashboard (docs/estate-hub.md). The page holds
// no data: it reads the v8 management API with the key the user enters.
func (s *Server) serveHubDashboard(c *gin.Context) {
	if !s.managementAvailable(c) {
		return
	}
	if cfg := s.getConfig(); cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", hubDashboardHTML)
}
