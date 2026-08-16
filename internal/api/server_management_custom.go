package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
)

func (s *Server) registerCustomManagementRoutes(mgmt *gin.RouterGroup) {
	if s == nil || s.mgmt == nil || mgmt == nil {
		return
	}
	mgmt.GET("/request-log-usage", s.mgmt.GetRequestLogUsage)
	mgmt.GET("/log-qa/status", s.mgmt.GetLogQAStatus)
	mgmt.GET("/log-qa/summary", s.mgmt.GetLogQASummary)
	mgmt.GET("/log-qa/sessions", s.mgmt.GetLogQASessions)
	mgmt.GET("/log-qa/sessions/logs", s.mgmt.GetLogQASessionLogs)
	mgmt.GET("/log-qa/runs", s.mgmt.GetLogQARuns)
	mgmt.POST("/log-qa/run", s.mgmt.PostLogQARun)
}

func (s *Server) registerCustomManagementAssetRoutes() {
	if s == nil || s.engine == nil {
		return
	}
	s.engine.HEAD("/management.html", s.serveManagementControlPanel)
	s.engine.GET(managementasset.RequestLogUsageScriptPath, s.serveRequestLogUsageScript)
	s.engine.HEAD(managementasset.RequestLogUsageScriptPath, s.serveRequestLogUsageScript)
	s.engine.GET(managementasset.LogQAScriptPath, s.serveLogQAScript)
	s.engine.HEAD(managementasset.LogQAScriptPath, s.serveLogQAScript)
}

func isCustomManagementAssetPath(path string) bool {
	return path == managementasset.RequestLogUsageScriptPath ||
		path == managementasset.LogQAScriptPath
}
