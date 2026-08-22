package api

func (s *Server) registerPublicOAuthInviteRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	s.engine.GET("/oauth/invite/:token", s.managementAvailabilityMiddleware(), s.mgmt.ServePublicOAuthInvite)
	s.engine.GET("/v0/oauth/invites/:token", s.managementAvailabilityMiddleware(), s.mgmt.GetPublicOAuthInvite)
	s.engine.POST("/v0/oauth/invites/:token/start", s.managementAvailabilityMiddleware(), s.mgmt.StartPublicOAuthInvite)
	s.engine.GET("/v0/oauth/invites/:token/status", s.managementAvailabilityMiddleware(), s.mgmt.GetPublicOAuthInviteStatus)
	s.engine.POST("/v0/oauth/invites/:token/callback", s.managementAvailabilityMiddleware(), s.mgmt.PostPublicOAuthInviteCallback)
}
