package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
)

// registerManagementV8Routes defines the v8 contract independently of v0.
// Configuration paths mirror the v8 YAML tree; operational routes use its groups.
func (s *Server) registerManagementV8Routes() {
	const prefix = "/v8/management"
	s.engine.GET(prefix+"/oauth/callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallbackV8)
	s.engine.POST(prefix+"/oauth/callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallbackV8)

	v8 := s.engine.Group(prefix)
	v8.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware(), func(c *gin.Context) {
		c.Set(management.ConfigV8ContextKey, true)
	})
	v8.GET("/config", s.mgmt.ConfigV8)
	v8.PUT("/config", s.mgmt.ConfigV8)
	v8.PATCH("/config", s.mgmt.ConfigV8)
	v8.GET("/config.yaml", s.mgmt.ConfigV8)
	v8.PUT("/config.yaml", s.mgmt.ConfigV8)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		v8.Handle(method, "/config/*path", s.mgmt.ConfigV8)
	}

	v8.GET("/server/latest-version", s.mgmt.GetLatestVersion)
	v8.POST("/server/shutdown", s.mgmt.ShutdownServer)
	v8.GET("/server/lockouts", s.mgmt.GetLockouts)
	v8.DELETE("/server/lockouts", s.mgmt.DeleteLockouts)
	v8.GET("/usage/windows", s.mgmt.GetUsageWindows)
	v8.POST("/usage/refresh", s.mgmt.RefreshUsageWindows)
	v8.POST("/usage/reset-credit", s.mgmt.UseResetCredit)
	v8.POST("/usage/start-window", s.mgmt.StartUsageWindow)
	v8.GET("/usage/events", s.mgmt.DesktopEvents)
	v8.GET("/routing/preferred-auth", s.mgmt.GetPreferredAuths)
	v8.PUT("/routing/preferred-auth", s.mgmt.PutPreferredAuth)
	v8.POST("/routing/move-session", s.mgmt.MoveSession)
	v8.POST("/requests/api-call", s.mgmt.APICall)
	v8.POST("/routing/cooldown/reset", s.mgmt.ResetQuota)
	v8.GET("/routing/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
	v8.POST("/routing/model-definitions/refresh", s.mgmt.RefreshModelDefinitions)
	v8.POST("/models/refresh", s.mgmt.RefreshModelDefinitions)

	v8.GET("/observability/logs", s.mgmt.GetLogs)
	v8.DELETE("/observability/logs", s.mgmt.DeleteLogs)
	v8.GET("/observability/logs/errors", s.mgmt.GetRequestErrorLogs)
	v8.GET("/observability/logs/errors/:name", s.mgmt.DownloadRequestErrorLog)
	v8.GET("/observability/logs/requests/:id", s.mgmt.GetRequestLogByID)
	v8.PUT("/observability/captures/lease", s.mgmt.LeaseCaptures)
	v8.GET("/observability/captures", s.mgmt.GetCaptures)
	v8.POST("/observability/captures/ack", s.mgmt.AcknowledgeCaptures)
	v8.DELETE("/observability/captures", s.mgmt.ClearCaptures)
	v8.GET("/observability/usage/api-keys", s.mgmt.GetAPIKeyUsage)
	v8.GET("/observability/usage/queue", s.mgmt.GetUsageQueue)

	v8.GET("/credentials", s.mgmt.ListAuthFiles)
	v8.POST("/credentials", s.mgmt.UploadAuthFileV8)
	v8.DELETE("/credentials", s.mgmt.DeleteAuthFileV8)
	v8.GET("/credentials/models", s.mgmt.GetAuthFileModels)
	v8.GET("/credentials/download", s.mgmt.DownloadAuthFile)
	v8.PATCH("/credentials/status", s.mgmt.PatchAuthFileStatus)
	v8.PATCH("/credentials/fields", s.mgmt.PatchAuthFileFields)
	v8.POST("/credentials/refresh", s.mgmt.RefreshAuthFiles)
	v8.POST("/credentials/verify", s.mgmt.VerifyAuthFileV8)
	v8.POST("/oauth/import", s.mgmt.ImportOAuthV8)
	v8.GET("/oauth/auth-url", s.mgmt.StartOAuthV8)
	v8.POST("/oauth/auth-url", s.mgmt.StartOAuthV8)
	v8.POST("/oauth/siwc/bind", s.mgmt.StartSIWCBind)
	v8.GET("/oauth/siwc/status", s.mgmt.GetSIWCStatus)
	v8.GET("/credentials/siwc/status", s.mgmt.GetSIWCStatus)
	v8.GET("/oauth/status", s.mgmt.GetAuthStatus)
	v8.DELETE("/oauth/session", s.mgmt.CancelAuthSession)

	v8.GET("/plugins", s.mgmt.ListPlugins)
	v8.DELETE("/plugins/:id", s.mgmt.DeletePlugin)
	v8.GET("/plugins/store", s.mgmt.ListPluginStore)
	v8.POST("/plugins/store/:id/install", s.mgmt.InstallPluginFromStore)
	v8.GET("/plugins/:id/quota", s.mgmt.GetPluginQuota)
	v8.POST("/plugins/:id/quota", s.mgmt.FetchPluginQuota)
	v8.DELETE("/plugins/:id/quota", s.mgmt.ResetPluginQuota)
}
